// Package app wires the master together and provides its command line interface.
package app

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/settings"
	"github.com/QwikByte/noryx/internal/pki"
)

type config struct {
	dataDir        string
	httpAddr       string
	tlsCert        string
	tlsKey         string
	trustedProxies []string
	restartCode    int
	enrollAddr     string
	publicAddr     string
	prereleases    bool
	log            logging.Options
}

// Command returns the root command of noryx-master.
func Command() *cobra.Command {
	var cfg config
	root := &cobra.Command{
		Use:          "noryx-master",
		Short:        "Admin panel and control plane of Noryx",
		Version:      buildinfo.Version,
		SilenceUsage: true,
		// main prints errors, except a restart.
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&cfg.dataDir, "data-dir", "/var/lib/noryx-master", "directory for the database and the certificate authority")

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the admin panel and the enrollment endpoint for agents",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), cfg) },
	}
	f := serve.Flags()
	f.StringVar(&cfg.httpAddr, "http-addr", "127.0.0.1:8080", "listen address of the admin panel; the panel's settings can replace it from the next start")
	f.StringVar(&cfg.tlsCert, "tls-cert", "", "TLS certificate of the admin panel; omit behind a TLS-terminating reverse proxy")
	f.StringVar(&cfg.tlsKey, "tls-key", "", "TLS private key of the admin panel")
	f.StringSliceVar(&cfg.trustedProxies, "trusted-proxy", nil, "IP addresses or CIDR networks of reverse proxies whose X-Forwarded-For or X-Real-IP header tells the client's address for the sign-in rate limit, the log and the users' sessions, e.g. 127.0.0.1")
	f.IntVar(&cfg.restartCode, "restart-exit-code", 0, "exit code for which the service manager starts the master again, e.g. systemd's RestartForceExitStatus; with it, administrators can restart the master from the panel")
	f.StringVar(&cfg.enrollAddr, "enroll-addr", ":9443", "listen address of the enrollment endpoint")
	f.StringVar(&cfg.publicAddr, "public-enroll-addr", "", "host:port agents use to reach the enrollment endpoint (default <hostname>:<enroll port>); the panel's settings can replace it")
	f.BoolVar(&cfg.prereleases, "prereleases", false, "offer pre-releases as updates too, e.g. on a test panel; updates from the panel install them only if this option is in /etc/noryx/master.env, which their installer reads")
	cfg.log.AddFlags(f)

	user := &cobra.Command{Use: "user", Short: "Manage administrator accounts"}
	user.AddCommand(&cobra.Command{
		Use:   "add <username>",
		Short: "Create an administrator account (reads the password from the terminal or stdin), e.g. the first one or after losing access",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return addUser(cmd.Context(), cfg, args[0]) },
	})

	// The log on the master's host shows everything, like the panel to administrators.
	logsCmd := logs.Command(func() (*logs.Store, error) {
		db, err := openDB(cfg.dataDir)
		return logs.NewStore(db, nil, nil), err
	})
	logsCmd.PreRun = func(cmd *cobra.Command, _ []string) {
		cmd.SetContext(access.WithGrants(cmd.Context(), access.Admin()))
	}

	var enrollAddr string
	addNodeCmd := &cobra.Command{
		Use:   "add <name> <agent-address>",
		Short: "Add a node and print the join token its agent enrolls with, e.g. for scripts",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return addNode(cmd.Context(), cfg, args[0], args[1], enrollAddr)
		},
	}
	addNodeCmd.Flags().StringVar(&enrollAddr, "public-enroll-addr", "", "host:port the agent reaches the enrollment endpoint at; the panel's settings can replace it")
	nodeCmd := &cobra.Command{Use: "node", Short: "Manage the nodes"}
	nodeCmd.AddCommand(addNodeCmd)

	backupCmd := &cobra.Command{
		Use:   "backup <file>",
		Short: "Save the database and the certificate authority to a .tar.gz file, or to stdout with -, while the master may run",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return backupMaster(cmd.Context(), cfg, args[0]) },
	}
	restoreCmd := &cobra.Command{
		Use: "restore <file>",
		Short: "Replace the database and the certificate authority with those of an archive of noryx-master backup or of the " +
			"backup job of the master, while the master is stopped. An encrypted archive's passphrase is read from the terminal, " +
			"or from " + passphraseEnv,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return restoreMaster(cfg, args[0]) },
	}

	root.AddCommand(serve, user, nodeCmd, logsCmd, backupCmd, restoreCmd)
	return root
}

// openDB opens the database in dataDir, see checkDataDir.
func openDB(dataDir string) (*sql.DB, error) {
	if err := checkDataDir(dataDir); err != nil {
		return nil, err
	}
	return database.Open(filepath.Join(dataDir, "master.db"))
}

// checkDataDir creates dataDir if it is missing, and checks that it belongs to the user: files
// another user, such as root, creates in it would lock the master out.
func checkDataDir(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return err
	}
	if owner, ok := ownerOf(info); ok && owner != strconv.Itoa(os.Getuid()) {
		if u, err := user.LookupId(owner); err == nil {
			owner = u.Username
		}
		return fmt.Errorf("%s belongs to another user, run this as %s, e.g. with: sudo -u %s noryx-master …", dataDir, owner, owner)
	}
	return nil
}

// addNode adds a node and prints its join token, alone on stdout for scripts.
func addNode(ctx context.Context, cfg config, name, address, enrollAddr string) error {
	db, err := openDB(cfg.dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	ca, err := pki.LoadOrCreateCA(filepath.Join(cfg.dataDir, "pki"))
	if err != nil {
		return err
	}
	conf, err := settings.Load(ctx, db, settings.Master{EnrollAddr: enrollAddr}, nil)
	if err != nil {
		return err
	}
	if conf.EnrollAddr() == "" {
		return errors.New("set --public-enroll-addr or the enrollment address in the panel's settings")
	}
	n, token, err := node.NewService(db, ca, nil, conf).Create(ctx, name, address)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Added node %s. Its agent enrolls once until %s with: noryx-agent enroll <join-token>\n",
		n.Name, token.ExpiresAt.Local().Format(time.DateTime))
	fmt.Println(token)
	return nil
}

func addUser(ctx context.Context, cfg config, username string) error {
	password, err := readPassword()
	if err != nil {
		return err
	}
	db, err := openDB(cfg.dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	// The password needs as many characters as the settings say.
	conf, err := settings.Load(ctx, db, settings.Master{}, nil)
	if err != nil {
		return err
	}
	user, err := auth.NewService(db, conf).CreateUser(ctx, username, password)
	if err == nil {
		err = access.NewService(db).MakeAdmin(ctx, user.ID)
	}
	if err == nil {
		fmt.Printf("Created administrator %q. Invite further users in the panel.\n", username)
	}
	return err
}

// readPassword prompts twice on a terminal and reads a single line from piped stdin.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "\nRepeat password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
