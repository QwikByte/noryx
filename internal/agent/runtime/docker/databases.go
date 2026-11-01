package docker

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// dialect is how the agent manages the databases of an engine with its own clients in the
// container. SQL goes to the clients' standard input, so that passwords stay out of the
// processes' arguments. The names of databases and users, and passwords, need no quoting,
// as noryxv1.DatabaseName and noryxv1.DatabasePassword match them.
type dialect struct {
	// superuser runs a client as the superuser, which reads SQL from its standard input and
	// prints rows as lines of tab-separated values.
	superuser func(password string) (cmd, env []string)
	databases string
	ensure    func(name, password string) string
	drop      func(name string) string
	// credential selects the hash of a user's password, which login turns into SQL that
	// creates the user with it again, or sets it.
	credential func(name string) string
	valid      *regexp.Regexp
	login      func(name, credential string, create bool) string
	// recreate drops a database and creates it empty, owned by its user.
	recreate func(name string) string
	dump     func(name string) []string
	// load runs a client as a database's user, which reads SQL from its standard input
	// without running commands of its own, as input turns it into, if set. For an engine
	// whose users sign in with a password there, password sets one.
	load     func(name, password string) (cmd, env []string)
	input    func(r io.Reader) io.Reader
	password func(name, password string) string
	// connections counts the clients connected, without the one that asks.
	connections string
}

var dialects = map[noryxv1.DatastoreEngine]dialect{
	noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB: {
		superuser: func(password string) ([]string, []string) {
			return []string{"mariadb", "-uroot", "-N", "-B"}, []string{"MYSQL_PWD=" + password}
		},
		databases: "SHOW DATABASES",
		ensure: func(n, p string) string {
			return fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%[1]s`; CREATE USER IF NOT EXISTS '%[1]s'@'%%' IDENTIFIED BY '%[2]s';"+
				" ALTER USER '%[1]s'@'%%' IDENTIFIED BY '%[2]s'; GRANT ALL PRIVILEGES ON `%[3]s`.* TO '%[1]s'@'%%';", n, p, grantable(n))
		},
		drop: func(n string) string {
			return fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`; DROP USER IF EXISTS '%[1]s'@'%%';", n)
		},
		credential: func(n string) string {
			return fmt.Sprintf("SELECT CONCAT(JSON_VALUE(Priv, '$.plugin'), ' ', JSON_VALUE(Priv, '$.authentication_string')) FROM mysql.global_priv WHERE User = '%s' AND Host = '%%'", n)
		},
		valid: regexp.MustCompile(`^[a-z0-9_]{1,64} [A-Za-z0-9*$./+=:]{0,512}$`),
		login: func(n, c string, create bool) string {
			plugin, hash, _ := strings.Cut(c, " ")
			if create {
				return fmt.Sprintf("CREATE USER '%[1]s'@'%%' IDENTIFIED VIA %[2]s USING '%[3]s'; GRANT ALL PRIVILEGES ON `%[4]s`.* TO '%[1]s'@'%%';", n, plugin, hash, grantable(n))
			}
			return fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED VIA %s USING '%s';", n, plugin, hash)
		},
		recreate: func(n string) string {
			return fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`; CREATE DATABASE `%[1]s`;", n)
		},
		dump: func(n string) []string {
			return []string{"mariadb-dump", "-uroot", "--single-transaction", "--routines", "--triggers", "--events", "--hex-blob", n}
		},
		// Binary mode refuses the commands of the client itself but DELIMITER, \C and the
		// sandbox's, e.g. to run programs, read files or connect elsewhere, which the sandbox
		// refuses too, and the client sends no files for LOAD DATA LOCAL.
		load: func(n, p string) ([]string, []string) {
			return []string{"mariadb", "--binary-mode", "--sandbox", "--local-infile=0", "--protocol=tcp", "-h127.0.0.1", "-u" + n, n},
				[]string{"MYSQL_PWD=" + p}
		},
		password:    func(n, p string) string { return fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s';", n, p) },
		connections: "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() AND COMMAND <> 'Daemon'",
	},
	// The superuser and the users of databases sign in without a password on the container
	// itself, as the image allows it there.
	noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES: {
		superuser: func(string) ([]string, []string) {
			return slices.Concat(psql, []string{"-A", "-t", "-U", "postgres", "-d", "postgres"}), nil
		},
		databases: "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY 1",
		// Only the user of a database may connect to it, and none to the superuser's.
		ensure: func(n, p string) string {
			return fmt.Sprintf("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s';"+
				" ELSE ALTER ROLE %[1]s LOGIN PASSWORD '%[2]s'; END IF; END $$;\n"+
				"SELECT 'CREATE DATABASE %[1]s OWNER %[1]s' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%[1]s')\\gexec\n"+
				"REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; REVOKE ALL ON DATABASE postgres FROM PUBLIC;", n, p)
		},
		drop: func(n string) string {
			return fmt.Sprintf("DROP DATABASE IF EXISTS %[1]s WITH (FORCE); DROP ROLE IF EXISTS %[1]s;", n)
		},
		credential: func(n string) string { return fmt.Sprintf("SELECT rolpassword FROM pg_authid WHERE rolname = '%s'", n) },
		valid:      regexp.MustCompile(`^SCRAM-SHA-256\$[0-9]{1,7}:[A-Za-z0-9+/=]{1,128}\$[A-Za-z0-9+/=]{1,128}:[A-Za-z0-9+/=]{1,128}$`),
		login: func(n, c string, create bool) string {
			if create {
				return fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s';", n, c)
			}
			return fmt.Sprintf("ALTER ROLE %s PASSWORD '%s';", n, c)
		},
		recreate: func(n string) string {
			return fmt.Sprintf("DROP DATABASE IF EXISTS %[1]s WITH (FORCE);\nCREATE DATABASE %[1]s OWNER %[1]s;\n"+
				"REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; REVOKE ALL ON DATABASE postgres FROM PUBLIC;", n)
		},
		dump: func(n string) []string {
			return []string{"pg_dump", "-U", "postgres", "--no-owner", "--no-privileges", n}
		},
		load: func(n, _ string) ([]string, []string) {
			return slices.Concat(psql, []string{"-U", n, "-d", n}), nil
		},
		input:       restricted,
		connections: "SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()",
	},
}

// psql runs PostgreSQL's client so that it stops at the first error, which it tells without
// the statement or the data it was about, as both may hold secrets.
var psql = []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=terse", "-v", "SHOW_CONTEXT=never"}

// grantable returns the name of a database in MariaDB's grants, where _ matches any character
// unless escaped.
func grantable(name string) string { return strings.ReplaceAll(name, "_", `\_`) }

// systemDatabases are the databases of MariaDB itself.
var systemDatabases = []string{"information_schema", "mysql", "performance_schema", "sys"}

// maxStderr is how much of what a client writes to its standard error an error keeps.
const maxStderr = 4 << 10

// exec runs a command in the container of a datastore as the image's user, with in as its
// standard input if not nil, and copies its standard output to out if not nil. It fails
// with what the command wrote to its standard error, without the secrets, as clients repeat
// the statement that failed.
func (d *Docker) exec(ctx context.Context, id string, cmd, env []string, in io.Reader, out io.Writer, secrets ...string) error {
	return d.run(ctx, datastoreName(id), datastoreUser, cmd, env, in, out, secrets...)
}

// run runs a command in a container as user, like exec.
func (d *Docker) run(ctx context.Context, name, user string, cmd, env []string, in io.Reader, out io.Writer, secrets ...string) error {
	res, err := d.cli.ExecCreate(ctx, name, client.ExecCreateOptions{
		User: user, Cmd: cmd, Env: env, AttachStdin: in != nil, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return notFound(err)
	}
	att, err := d.cli.ExecAttach(ctx, res.ID, client.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer att.Close()
	defer context.AfterFunc(ctx, att.Close)()
	sent := make(chan error, 1)
	go func() {
		var err error
		if in != nil {
			_, err = io.Copy(att.Conn, in)
			err = errors.Join(err, att.CloseWrite())
		}
		sent <- err
	}()
	stderr := &limited{max: maxStderr}
	if _, err := stdcopy.StdCopy(cmp.Or(out, io.Discard), stderr, att.Reader); err != nil {
		return cmp.Or(ctx.Err(), err)
	}
	// A dump that couldn't be read to its end must not count as loaded.
	if err := <-sent; err != nil {
		return fmt.Errorf("%s: %w", cmd[0], err)
	}
	inspect, err := d.cli.ExecInspect(ctx, res.ID, client.ExecInspectOptions{})
	if err != nil {
		return err
	}
	if inspect.ExitCode != 0 {
		msg := stderr.String()
		for _, secret := range secrets {
			if secret != "" {
				msg = strings.ReplaceAll(msg, secret, "<hidden>")
			}
		}
		return fmt.Errorf("%s failed: %s", cmd[0], strings.TrimSpace(msg))
	}
	return nil
}

// limited keeps the first bytes written to it.
type limited struct {
	bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		l.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// session runs the clients of a ready datastore as its superuser.
type session struct {
	d        *Docker
	id       string
	engine   noryxv1.DatastoreEngine
	dialect  dialect
	password string
}

func (d *Docker) session(ctx context.Context, id string, names ...string) (*session, error) {
	for _, n := range names { // the service checks them before
		if problem := noryxv1.DatabaseNameProblem(n); problem != "" {
			return nil, fmt.Errorf("%q can't name a database: %s", n, problem)
		}
	}
	spec, dir, err := d.ready(ctx, id)
	if err != nil {
		return nil, err
	}
	password, err := os.ReadFile(filepath.Join(dir, superuserFile)) //nolint:gosec // in the folder of the datastore
	return &session{d, id, spec.Engine, dialects[spec.Engine], strings.TrimSpace(string(password))}, err
}

// query runs SQL with secrets in it and returns the words it prints.
func (s *session) query(ctx context.Context, sql string, secrets ...string) ([]string, error) {
	cmd, env := s.dialect.superuser(s.password)
	var out bytes.Buffer
	err := s.d.exec(ctx, s.id, cmd, env, strings.NewReader(sql), &out, append(secrets, s.password)...)
	return strings.Fields(out.String()), err
}

// credential returns the hash of the password of a database's user, checked to be safe in SQL.
func (s *session) credential(ctx context.Context, name string) (string, error) {
	words, err := s.query(ctx, s.dialect.credential(name))
	if line := strings.Join(words, " "); err != nil || s.dialect.valid.MatchString(line) {
		return line, err
	}
	return "", fmt.Errorf("the user %s has no password Noryx can keep", name)
}

func (d *Docker) Databases(ctx context.Context, id string) ([]string, error) {
	s, err := d.session(ctx, id)
	if err != nil {
		return nil, err
	}
	names, err := s.query(ctx, s.dialect.databases)
	names = slices.DeleteFunc(names, func(n string) bool { return slices.Contains(systemDatabases, n) })
	slices.Sort(names)
	return names, err
}

func (d *Docker) EnsureDatabase(ctx context.Context, id, name, password string) error {
	if !noryxv1.DatabasePassword.MatchString(password) {
		return errors.New("invalid password")
	}
	s, err := d.session(ctx, id, name)
	if err == nil {
		_, err = s.query(ctx, s.dialect.ensure(name, password), password)
	}
	return err
}

func (d *Docker) DropDatabase(ctx context.Context, id, name string) error {
	s, err := d.session(ctx, id, name)
	if err == nil {
		_, err = s.query(ctx, s.dialect.drop(name))
	}
	return err
}

func (d *Docker) Dump(ctx context.Context, id, name string, w io.Writer) error {
	s, err := d.session(ctx, id, name)
	if err != nil {
		return err
	}
	_, env := s.dialect.superuser(s.password)
	return d.exec(ctx, id, s.dialect.dump(name), env, nil, w, s.password)
}

func (d *Docker) Load(ctx context.Context, id, name string, r io.Reader) (err error) {
	s, err := d.session(ctx, id, name)
	if err != nil {
		return err
	}
	cred, err := s.credential(ctx, name)
	if err != nil {
		return err
	}
	if _, err := s.query(ctx, s.dialect.recreate(name)); err != nil {
		return err
	}
	password := ""
	if s.dialect.password != nil {
		// The user signs in with a password, which only the master knows, so it gets another
		// one for the time of the load.
		password = strings.ToLower(rand.Text())
		if _, err := s.query(ctx, s.dialect.password(name, password), password); err != nil {
			return err
		}
		defer func() {
			_, restoreErr := s.query(context.WithoutCancel(ctx), s.dialect.login(name, cred, false), cred)
			err = errors.Join(err, restoreErr)
		}()
	}
	cmd, env := s.dialect.load(name, password)
	if s.dialect.input != nil {
		r = s.dialect.input(r)
	}
	return d.exec(ctx, id, cmd, env, r, nil, password)
}

// connections counts the clients of a ready datastore.
func (d *Docker) connections(ctx context.Context, id string) (uint32, error) {
	s, err := d.session(ctx, id)
	if err != nil {
		return 0, err
	}
	words, err := s.query(ctx, s.dialect.connections)
	if err != nil {
		return 0, err
	}
	if len(words) != 1 {
		return 0, fmt.Errorf("unexpected output %q", words)
	}
	n, err := strconv.ParseUint(words[0], 10, 32)
	return uint32(n), err
}

var _ runtime.Datastores = (*Docker)(nil)
