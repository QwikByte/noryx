package docker

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	// maxValue is how many characters of a value a page shows.
	maxValue = 200
	// maxPage is how many bytes the rows of a page may have, below the 4 MiB that gRPC accepts
	// by default.
	maxPage = 3 << 20
	// browseTimeout limits each statement of a browsing session, in seconds.
	browseTimeout = 10
)

// column is a column of a table as an engine describes it.
type column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Data string `json:"data"` // MariaDB's data type, e.g. blob
	Key  string `json:"key"`  // PRI for one of the primary key
}

// browser is how the agent reads the tables of an engine's databases: never as the superuser,
// as reading runs what the database's user may have written, e.g. casts, functions and views,
// in a session that only reads, with statements it builds itself from names that need no
// escaping, as noryxv1.DatabaseName and noryxv1.TableName match them, and values in
// hexadecimal. Each statement prints a JSON value per line.
type browser struct {
	// client reads SQL from its standard input on a database as user, who signs in with
	// password if it needs one.
	client func(user, database, password string) (cmd, env []string)
	// reader, if set, creates a user who may only read a database and signs in with password
	// on the container, and forget drops it; otherwise the database's own user reads it.
	reader  func(user, database, password string) string
	forget  func(user string) string
	session string
	quote   func(name string) string
	// tables prints a noryxv1.Table per line, columns a column.
	tables  func(database string) string
	columns func(database, schema, table string) string
	// text is a column's value as text, as a page shows it; cell cuts it to at most
	// maxValue+1 characters.
	text, cell func(c column) string
	// equals and contains match text with a value in hexadecimal: contains ignores case.
	equals, contains func(text, value string) string
	// from names a table.
	from func(schema, table string) string
	// row prints an array of cells.
	row func(cells []string) string
}

// mariadbBinary are the data types of MariaDB without a character set, which a page shows as
// hexadecimal numbers.
var mariadbBinary = []string{"binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry", "point",
	"linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection"}

var browsers = map[noryxv1.DatastoreEngine]browser{
	noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB: {
		// The sandbox refuses the commands of the client itself, and raw output keeps JSON as it is.
		client: func(user, db, password string) ([]string, []string) {
			return []string{"mariadb", "-u" + user, "-N", "-B", "-r", "--sandbox", "--default-character-set=utf8mb4", db}, []string{"MYSQL_PWD=" + password}
		},
		// MariaDB signs in its users only with their passwords, which only the master knows.
		reader: func(user, db, password string) string {
			return fmt.Sprintf("CREATE USER '%[1]s'@'localhost' IDENTIFIED BY '%[3]s'; GRANT SELECT ON `%[2]s`.* TO '%[1]s'@'localhost';", user, grantable(db), password)
		},
		forget:  func(user string) string { return fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost';", user) },
		session: fmt.Sprintf("SET SESSION TRANSACTION READ ONLY; SET SESSION max_statement_time = %d;\n", browseTimeout),
		quote:   func(name string) string { return "`" + name + "`" },
		tables: func(db string) string {
			return fmt.Sprintf("SELECT JSON_OBJECT('name', TABLE_NAME, 'rows', IFNULL(TABLE_ROWS, -1), 'size', IFNULL(DATA_LENGTH + INDEX_LENGTH, 0))"+
				" FROM information_schema.TABLES WHERE TABLE_SCHEMA = '%s' AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME;", db)
		},
		columns: func(db, _, table string) string {
			return fmt.Sprintf("SELECT JSON_OBJECT('name', COLUMN_NAME, 'type', COLUMN_TYPE, 'data', DATA_TYPE, 'key', COLUMN_KEY)"+
				" FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = '%s' AND TABLE_NAME = '%s' ORDER BY ORDINAL_POSITION;", db, table)
		},
		text: func(c column) string {
			if slices.Contains(mariadbBinary, c.Data) {
				return fmt.Sprintf("CONCAT('0x', HEX(`%s`))", c.Name)
			}
			return fmt.Sprintf("`%s`", c.Name)
		},
		cell: func(c column) string {
			if slices.Contains(mariadbBinary, c.Data) {
				return fmt.Sprintf("CONCAT('0x', HEX(LEFT(`%s`, %d)))", c.Name, (maxValue-2)/2+1)
			}
			return fmt.Sprintf("LEFT(`%s`, %d)", c.Name, maxValue+1)
		},
		// Compared as the bytes of UTF-8, whatever the collation of the column.
		equals: func(text, value string) string {
			return fmt.Sprintf("CAST(CONVERT(%s USING utf8mb4) AS BINARY) = x'%s'", text, value)
		},
		contains: func(text, value string) string {
			return fmt.Sprintf("LOCATE(CAST(LOWER(_utf8mb4 x'%s') AS BINARY), CAST(LOWER(CONVERT(%s USING utf8mb4)) AS BINARY)) > 0", value, text)
		},
		from: func(_, table string) string { return "`" + table + "`" },
		row:  func(cells []string) string { return "JSON_ARRAY(" + strings.Join(cells, ", ") + ")" },
	},
	noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES: {
		// The database's user signs in without a password on the container, as Load does.
		client: func(user, db, _ string) ([]string, []string) {
			return slices.Concat(psql, []string{"-A", "-t", "-U", user, "-d", db}), nil
		},
		session: fmt.Sprintf("SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY; SET statement_timeout = '%ds';\n", browseTimeout),
		quote:   func(name string) string { return `"` + name + `"` },
		tables: func(string) string {
			return "SELECT json_build_object('schema', n.nspname, 'name', c.relname, 'rows', c.reltuples::bigint, 'size', pg_total_relation_size(c.oid))" +
				" FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace" +
				" WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_' ORDER BY n.nspname, c.relname;"
		},
		columns: func(_, schema, table string) string {
			return fmt.Sprintf("SELECT json_build_object('name', a.attname, 'type', format_type(a.atttypid, a.atttypmod), 'key', CASE WHEN a.attnum = ANY(i.indkey) THEN 'PRI' ELSE '' END)"+
				" FROM pg_attribute a LEFT JOIN pg_index i ON i.indrelid = a.attrelid AND i.indisprimary"+
				" WHERE a.attrelid = to_regclass('\"%s\".\"%s\"') AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum;", schema, table)
		},
		text: func(c column) string { return fmt.Sprintf(`%q::text`, c.Name) },
		cell: func(c column) string { return fmt.Sprintf(`left(%q::text, %d)`, c.Name, maxValue+1) },
		equals: func(text, value string) string {
			return fmt.Sprintf("%s = convert_from(decode('%s', 'hex'), 'UTF8')", text, value)
		},
		contains: func(text, value string) string {
			return fmt.Sprintf("strpos(lower(%s), lower(convert_from(decode('%s', 'hex'), 'UTF8'))) > 0", text, value)
		},
		from: func(schema, table string) string { return fmt.Sprintf("%q.%q", schema, table) },
		row:  func(cells []string) string { return "to_json(ARRAY[" + strings.Join(cells, ", ") + "]::text[])" },
	},
}

// rows returns the statement that prints the cells of each row that a request chooses, and
// one more, of the columns of its table, which have those it sorts and filters by.
func (b browser) rows(req *noryxv1.BrowseTableRequest, schema string, columns []column) string {
	cells := make([]string, len(columns))
	for i, c := range columns {
		cells[i] = b.cell(c)
	}
	sql := "SELECT " + b.row(cells) + " FROM " + b.from(schema, req.GetTable())
	if f := req.GetFilter(); f != nil {
		match := b.equals
		if f.GetContains() {
			match = b.contains
		}
		c := columns[slices.IndexFunc(columns, func(c column) bool { return c.Name == f.GetColumn() })]
		sql += " WHERE " + match(b.text(c), hex.EncodeToString([]byte(f.GetValue())))
	}
	var order []string
	if req.GetSort() != "" {
		order = append(order, b.quote(req.GetSort()))
		if req.GetDescending() {
			order[0] += " DESC"
		}
	}
	for _, c := range columns {
		if c.Key == "PRI" && c.Name != req.GetSort() {
			order = append(order, b.quote(c.Name))
		}
	}
	if len(order) > 0 {
		sql += " ORDER BY " + strings.Join(order, ", ")
	}
	return sql + fmt.Sprintf(" LIMIT %d OFFSET %d;", req.GetLimit()+1, req.GetOffset())
}

// browse runs a statement that sql builds on a database of a ready datastore, in a session
// that only reads, and decodes each line it prints with decode.
func (d *Docker) browse(ctx context.Context, id, name string, sql func(b browser) string, decode func(line []byte) error) (err error) {
	s, err := d.session(ctx, id, name)
	if err != nil {
		return err
	}
	b := browsers[s.engine]
	user, password := name, ""
	if b.reader != nil {
		user, password = "noryx-reader-"+strings.ToLower(rand.Text()[:12]), strings.ToLower(rand.Text())
		defer func() {
			_, forgetErr := s.query(context.WithoutCancel(ctx), b.forget(user))
			err = errors.Join(err, forgetErr)
		}()
		if _, err := s.query(ctx, b.reader(user, name, password), password); err != nil {
			return err
		}
	}
	cmd, env := b.client(user, name, password)
	out := &capped{max: maxPage}
	if err := d.exec(ctx, id, cmd, env, strings.NewReader(b.session+sql(b)), out, password); err != nil {
		return err
	}
	lines := bufio.NewScanner(&out.Buffer)
	lines.Buffer(nil, maxPage)
	for lines.Scan() {
		if err := decode(lines.Bytes()); err != nil {
			return fmt.Errorf("unexpected output: %w", err)
		}
	}
	return lines.Err()
}

var errTooLarge = fmt.Errorf("the rows are larger than %d MiB", maxPage>>20)

// capped keeps what is written to it, up to max bytes.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.Len()+len(p) > c.max {
		return 0, errTooLarge
	}
	return c.Buffer.Write(p)
}

func (d *Docker) Tables(ctx context.Context, id, name string) ([]*noryxv1.Table, error) {
	tables := []*noryxv1.Table{}
	err := d.browse(ctx, id, name, func(b browser) string { return b.tables(name) }, func(line []byte) error {
		t := &noryxv1.Table{}
		tables = append(tables, t)
		return protojson.Unmarshal(line, t)
	})
	return tables, err
}

func (d *Docker) Browse(ctx context.Context, id string, req *noryxv1.BrowseTableRequest) (*noryxv1.BrowseTableResponse, error) {
	name, table, schema := req.GetDatabase(), req.GetTable(), cmp.Or(req.GetSchema(), "public") // MariaDB has none
	if !noryxv1.TableName.MatchString(table) || !noryxv1.TableName.MatchString(schema) {
		return nil, runtime.ErrNoTable
	}
	if f := req.GetFilter(); f != nil && f.Problem() != "" {
		return nil, errors.New(f.Problem())
	}
	var columns []column
	err := d.browse(ctx, id, name, func(b browser) string { return b.columns(name, schema, table) }, func(line []byte) error {
		var c column
		if err := json.Unmarshal(line, &c); err != nil {
			return err
		}
		if !noryxv1.TableName.MatchString(c.Name) {
			return fmt.Errorf("noryx only shows columns whose names have letters, digits, _, $ and -, unlike %q", c.Name)
		}
		columns = append(columns, c)
		return nil
	})
	switch {
	case err != nil:
		return nil, err
	case len(columns) == 0:
		return nil, runtime.ErrNoTable
	}
	for _, name := range []string{req.GetSort(), req.GetFilter().GetColumn()} {
		if name != "" && !slices.ContainsFunc(columns, func(c column) bool { return c.Name == name }) {
			return nil, fmt.Errorf("%w: %q", runtime.ErrNoColumn, name)
		}
	}
	res := &noryxv1.BrowseTableResponse{}
	for _, c := range columns {
		res.Columns = append(res.Columns, &noryxv1.TableColumn{Name: c.Name, Type: c.Type, PrimaryKey: c.Key == "PRI"})
	}
	rows := func(b browser) string { return b.rows(req, schema, columns) }
	err = d.browse(ctx, id, name, rows, func(line []byte) error {
		var values []*string
		if err := json.Unmarshal(line, &values); err != nil {
			return err
		}
		if len(values) != len(columns) {
			return errors.New("a row doesn't fit the columns")
		}
		row := &noryxv1.TableRow{}
		for _, v := range values {
			row.Values = append(row.Values, value(v))
		}
		res.Rows = append(res.Rows, row)
		return nil
	})
	if len(res.Rows) > int(req.GetLimit()) {
		res.Rows, res.More = res.Rows[:req.GetLimit()], true
	}
	return res, err
}

// value cuts text to maxValue characters.
func value(text *string) *noryxv1.TableValue {
	if text == nil {
		return &noryxv1.TableValue{Null: true}
	}
	if r := []rune(*text); len(r) > maxValue {
		return &noryxv1.TableValue{Text: string(r[:maxValue]), Truncated: true}
	}
	return &noryxv1.TableValue{Text: *text}
}
