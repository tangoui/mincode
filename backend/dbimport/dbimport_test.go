package dbimport

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blowfish"
)

func fixture(t *testing.T, parts ...string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(append([]string{"testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func rowByName(t *testing.T, rows []Candidate, name string) Candidate {
	t.Helper()
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("row %q not found in %d rows", name, len(rows))
	return Candidate{}
}

// encryptDBeaverCredentials produces credentials-config.json bytes the way
// DBeaver does (random IV prefix, AES-128-CBC, PKCS#7).
func encryptDBeaverCredentials(t *testing.T, plain []byte) []byte {
	t.Helper()
	key, _ := hex.DecodeString(dbeaverCredentialsKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := []byte("0123456789abcdef")
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return append(iv, out...)
}

// dbeaverWorkspace copies the fixture project to a temp dir and adds the
// encrypted credentials file.
func dbeaverWorkspace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "General", ".dbeaver")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := fixture(t, "dbeaver", "General", ".dbeaver")
	data, err := os.ReadFile(filepath.Join(source, "data-sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data-sources.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile(filepath.Join(source, "credentials-plain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials-config.json"), encryptDBeaverCredentials(t, plain), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}

func TestDBeaverProjectImport(t *testing.T) {
	project := dbeaverWorkspace(t)
	for _, path := range []string{project, filepath.Join(project, ".dbeaver"), filepath.Join(project, ".dbeaver", "data-sources.json"), filepath.Dir(project)} {
		preview, err := buildPreview(SourceDBeaver, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(preview.Rows) != 5 || len(preview.Warnings) != 0 {
			t.Fatalf("%s: rows=%d warnings=%v", path, len(preview.Rows), preview.Warnings)
		}
	}
	preview, _ := buildPreview(SourceDBeaver, project)
	mysql := rowByName(t, preview.Rows, "Orders MySQL")
	if mysql.DbType != "mysql" || mysql.Status != StatusReady || mysql.Host != "10.0.0.5" || mysql.Port != 3307 ||
		mysql.Database != "orders" || mysql.Username != "orders_app" || !mysql.SSL || mysql.Folder != "Prod" {
		t.Fatalf("mysql row: %+v", mysql)
	}
	if !mysql.HasPassword || mysql.password != "fake-orders-pw" {
		t.Fatal("mysql password not decrypted")
	}
	if tunnel := mysql.Tunnel; tunnel == nil || tunnel.Host != "bastion.example.test" || tunnel.Port != 2222 ||
		tunnel.Username != "jump" || tunnel.AuthType != "password" || !tunnel.HasPassword || tunnel.password != "fake-jump-pw" {
		t.Fatalf("mysql tunnel: %+v", mysql.Tunnel)
	}
	pg := rowByName(t, preview.Rows, "Analytics PG")
	if pg.DbType != "postgres" || pg.Host != "pg.example.test" || pg.Port != 5433 || pg.Database != "analytics" ||
		!pg.SSL || pg.Username != "analyst" || pg.HasPassword || pg.Folder != "Prod/Reports" {
		t.Fatalf("pg row: %+v", pg)
	}
	sqlite := rowByName(t, preview.Rows, "Local cache")
	if sqlite.DbType != "sqlite" || sqlite.FilePath != "/tmp/fake/cache.db" || sqlite.Host != "" || sqlite.Port != 0 {
		t.Fatalf("sqlite row: %+v", sqlite)
	}
	hive := rowByName(t, preview.Rows, "Warehouse")
	if hive.Status != StatusNeedsPlugin || hive.PluginDriverID != "hive" || hive.Port != 10000 || hive.DbType != "" {
		t.Fatalf("hive row: %+v", hive)
	}
	db2 := rowByName(t, preview.Rows, "Mainframe")
	if db2.Status != StatusUnsupported || db2.SourceType != "db2 / db2_iseries" {
		t.Fatalf("db2 row: %+v", db2)
	}
	if mysql.PasswordUnavailable || !pg.PasswordUnavailable || !hive.PasswordUnavailable || sqlite.PasswordUnavailable || sqlite.Database != "" {
		t.Fatalf("password flags: mysql=%v pg=%v hive=%v sqlite=%+v", mysql.PasswordUnavailable, pg.PasswordUnavailable, hive.PasswordUnavailable, sqlite)
	}
}

func TestDBeaverMissingCredentialsWarns(t *testing.T) {
	project := dbeaverWorkspace(t)
	if err := os.Remove(filepath.Join(project, ".dbeaver", "credentials-config.json")); err != nil {
		t.Fatal(err)
	}
	preview, err := buildPreview(SourceDBeaver, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Warnings) != 1 || preview.Warnings[0] != WarnCredentialsMissing {
		t.Fatalf("warnings: %v", preview.Warnings)
	}
	if mysql := rowByName(t, preview.Rows, "Orders MySQL"); mysql.HasPassword || !mysql.PasswordUnavailable {
		t.Fatalf("mysql: %+v", mysql)
	}
	if rowByName(t, preview.Rows, "Local cache").PasswordUnavailable {
		t.Fatal("a file database needs no password")
	}
}

func TestDBeaverPrefersSSHOverProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-sources.json")
	content := `{"connections":{"pg-1":{"provider":"postgresql","driver":"postgres-jdbc","name":"Both","save-password":false,
		"configuration":{"host":"db.example.test","port":"5432","database":"app","user":"u","handlers":{
			"proxy":{"type":"PROXY","enabled":true,"properties":{"socks-host":"proxy.example.test","socks-port":"1080"}},
			"ssh_tunnel":{"type":"TUNNEL","enabled":true,"properties":{"host":"jump.example.test","port":"22","user":"ops"}}}}}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := buildPreview(SourceDBeaver, path)
	if err != nil {
		t.Fatal(err)
	}
	// No connection saves a password, so no credentials file is expected.
	if len(preview.Warnings) != 0 {
		t.Fatalf("warnings: %v", preview.Warnings)
	}
	row := rowByName(t, preview.Rows, "Both")
	if tunnel := row.Tunnel; tunnel == nil || tunnel.Type != "ssh" || tunnel.Host != "jump.example.test" || tunnel.Username != "ops" {
		t.Fatalf("tunnel: %+v", row.Tunnel)
	}
	if !row.PasswordUnavailable || len(row.Warnings) != 1 || row.Warnings[0] != WarnTunnelPartial {
		t.Fatalf("row: %+v", row)
	}
}

func TestPreviewJSONNeverContainsSecrets(t *testing.T) {
	project := dbeaverWorkspace(t)
	previews := []Preview{}
	for _, item := range []struct{ source, path string }{
		{SourceDBeaver, project},
		{SourceNavicat, fixture(t, "navicat", "connections.ncx")},
		{SourceNavicat, fixture(t, "navicat", "navicat11.ncx")},
		{SourceHeidiSQL, fixture(t, "heidisql", "settings.txt")},
	} {
		preview, err := buildPreview(item.source, item.path)
		if err != nil {
			t.Fatal(err)
		}
		previews = append(previews, preview)
	}
	encoded, err := json.Marshal(previews)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fake-orders-pw", "fake-jump-pw", "fake-mysql-pw", "fake-ssh-pw", "fake-mongo-pw", "This is a test", "fake-heidi-pw", "fake-tunnel-pw"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("preview JSON leaks %q", secret)
		}
	}
	if !strings.Contains(string(encoded), `"hasPassword":true`) {
		t.Fatal("preview should report hasPassword")
	}
}

func TestDBeaverUnreadableCredentialsWarns(t *testing.T) {
	project := dbeaverWorkspace(t)
	if err := os.WriteFile(filepath.Join(project, ".dbeaver", "credentials-config.json"), []byte("garbage-garbage-garbage-garbage!"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := buildPreview(SourceDBeaver, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Warnings) != 1 || preview.Warnings[0] != WarnCredentialsInvalid {
		t.Fatalf("warnings: %v", preview.Warnings)
	}
	if rowByName(t, preview.Rows, "Orders MySQL").HasPassword {
		t.Fatal("password must be absent without credentials")
	}
}

func TestNavicatImport(t *testing.T) {
	preview, err := buildPreview(SourceNavicat, fixture(t, "navicat", "connections.ncx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rows) != 5 {
		t.Fatalf("rows: %d", len(preview.Rows))
	}
	mysql := rowByName(t, preview.Rows, "Shop MySQL")
	if mysql.DbType != "mysql" || mysql.password != "fake-mysql-pw" || mysql.Host != "mysql.example.test" || mysql.Database != "shop" {
		t.Fatalf("mysql: %+v", mysql)
	}
	if mysql.Tunnel == nil || mysql.Tunnel.password != "fake-ssh-pw" || mysql.Tunnel.Username != "ops" || mysql.Tunnel.Port != 22 {
		t.Fatalf("tunnel: %+v", mysql.Tunnel)
	}
	mongo := rowByName(t, preview.Rows, "Docs Mongo")
	if mongo.DbType != "mongodb" || mongo.Port != 27018 || !mongo.SSL || mongo.password != "fake-mongo-pw" {
		t.Fatalf("mongo: %+v", mongo)
	}
	sqlite := rowByName(t, preview.Rows, "Local SQLite")
	if sqlite.DbType != "sqlite" || sqlite.FilePath != "/tmp/fake/local.sqlite" {
		t.Fatalf("sqlite: %+v", sqlite)
	}
	broken := rowByName(t, preview.Rows, "Broken")
	if broken.HasPassword || len(broken.Warnings) != 1 || broken.Warnings[0] != WarnPasswordUndecrypted || broken.DbType != "postgres" {
		t.Fatalf("broken: %+v", broken)
	}
	legacy := rowByName(t, preview.Rows, "Legacy DB2")
	if legacy.Status != StatusUnsupported {
		t.Fatal("DB2 must be unsupported")
	}
	if !broken.PasswordUnavailable || !legacy.PasswordUnavailable || mysql.PasswordUnavailable || mongo.PasswordUnavailable || sqlite.PasswordUnavailable {
		t.Fatal("only rows without a decrypted password may ask for one")
	}
}

func TestNavicatUnsavedPasswordAndMissingHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extra.ncx")
	content := `<Connections Ver="1.5">` +
		`<Connection ConnectionName="Unsaved" ConnType="MYSQL" Host="db.example.test" UserName="u" Password="CA8E6B2DC3FD32E29FB710C902F30C7E" SavePassword="false"/>` +
		`<Connection ConnectionName="Cluster" ConnType="MONGODB" UserName="u"/>` +
		`</Connections>`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := buildPreview(SourceNavicat, path)
	if err != nil {
		t.Fatal(err)
	}
	if unsaved := rowByName(t, preview.Rows, "Unsaved"); unsaved.HasPassword || !unsaved.PasswordUnavailable || len(unsaved.Warnings) != 0 {
		t.Fatalf("unsaved: %+v", unsaved)
	}
	if cluster := rowByName(t, preview.Rows, "Cluster"); cluster.Host != "localhost" || len(cluster.Warnings) != 1 || cluster.Warnings[0] != WarnHostMissing {
		t.Fatalf("cluster: %+v", cluster)
	}
}

func TestNavicat11BlowfishPassword(t *testing.T) {
	preview, err := buildPreview(SourceNavicat, fixture(t, "navicat", "navicat11.ncx"))
	if err != nil {
		t.Fatal(err)
	}
	// Published Navicat 11 test vector: "This is a test".
	if row := rowByName(t, preview.Rows, "Old MySQL"); row.password != "This is a test" {
		t.Fatal("navicat 11 password not decrypted")
	}
}

func navicat11Encrypt(t *testing.T, plain []byte) string {
	t.Helper()
	key := sha1.Sum([]byte(navicatBlowfishSeed))
	block, err := blowfish.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	vector := bytes.Repeat([]byte{0xFF}, 8)
	block.Encrypt(vector, vector)
	out := []byte{}
	full := len(plain) / 8 * 8
	for offset := 0; offset < full; offset += 8 {
		chunk := make([]byte, 8)
		for i := range chunk {
			chunk[i] = plain[offset+i] ^ vector[i]
		}
		block.Encrypt(chunk, chunk)
		for i := range vector {
			vector[i] ^= chunk[i]
		}
		out = append(out, chunk...)
	}
	if rest := plain[full:]; len(rest) > 0 {
		block.Encrypt(vector, vector)
		for i := range rest {
			out = append(out, rest[i]^vector[i])
		}
	}
	return strings.ToUpper(hex.EncodeToString(out))
}

func TestNavicatPasswordSchemes(t *testing.T) {
	aesValue, ok := decryptNavicatPassword("B75D320B6211468D63EB3B67C9E85933", false)
	if !ok || aesValue != "This is a test" {
		t.Fatalf("navicat 12 vector: %q %v", aesValue, ok)
	}
	for _, plain := range []string{"x", "exactly8", "sixteen-bytes-pw", "a longer fake password"} {
		got, ok := decryptNavicatPassword(navicat11Encrypt(t, []byte(plain)), true)
		if !ok || got != plain {
			t.Fatalf("blowfish round trip %q -> %q", plain, got)
		}
	}
	if _, ok := decryptNavicatPassword("not-hex", false); ok {
		t.Fatal("invalid hex must fail")
	}
}

func TestDataGripImport(t *testing.T) {
	originalHome := userHomeDir
	userHomeDir = func() (string, error) { return "/home/fake", nil }
	t.Cleanup(func() { userHomeDir = originalHome })
	for _, path := range []string{
		fixture(t, "datagrip", "options"),
		fixture(t, "datagrip"),
		fixture(t, "datagrip", "options", "dataSources.xml"),
	} {
		preview, err := buildPreview(SourceDataGrip, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(preview.Rows) != 4 {
			t.Fatalf("%s: rows=%d", path, len(preview.Rows))
		}
		pg := rowByName(t, preview.Rows, "pg@staging")
		if pg.DbType != "postgres" || pg.Host != "staging.example.test" || pg.Database != "app" || pg.Username != "stage_user" ||
			pg.Folder != "Staging" || !pg.PasswordUnavailable || pg.HasPassword {
			t.Fatalf("pg: %+v", pg)
		}
		if tunnel := pg.Tunnel; tunnel == nil || tunnel.Host != "gw.example.test" || tunnel.Port != 2200 || tunnel.Username != "deploy" || tunnel.AuthType != "key" ||
			tunnel.KeyPath != filepath.Join("/home/fake", ".ssh", "id_ed25519") {
			t.Fatalf("pg tunnel: %+v", pg.Tunnel)
		}
		mssql := rowByName(t, preview.Rows, "mssql")
		if mssql.DbType != "sqlserver" || mssql.Port != 1444 || mssql.Database != "erp" || !mssql.SSL || mssql.Username != "sa" {
			t.Fatalf("mssql: %+v", mssql)
		}
		oracle := rowByName(t, preview.Rows, "oracle")
		if oracle.DbType != "oracle" || oracle.Host != "ora.example.test" || oracle.Port != 1522 || oracle.Database != "ORCLPDB1" {
			t.Fatalf("oracle: %+v", oracle)
		}
		vertica := rowByName(t, preview.Rows, "vertica")
		if vertica.Status != StatusNeedsPlugin || vertica.PluginDriverID != "vertica" || vertica.Port != 5433 {
			t.Fatalf("vertica: %+v", vertica)
		}
	}
}

func TestDataGripProjectIdeaFolder(t *testing.T) {
	project := t.TempDir()
	idea := filepath.Join(project, ".idea")
	if err := os.MkdirAll(idea, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture(t, "datagrip", "options", "dataSources.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idea, "dataSources.xml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := buildPreview(SourceDataGrip, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rows) != 4 || rowByName(t, preview.Rows, "pg@staging").Tunnel != nil {
		t.Fatalf("rows: %+v", preview.Rows)
	}
}

func TestExpandJetBrainsPath(t *testing.T) {
	originalHome := userHomeDir
	t.Cleanup(func() { userHomeDir = originalHome })
	userHomeDir = func() (string, error) { return "/home/fake", nil }
	if got := expandJetBrainsPath("$USER_HOME$/.ssh/id_rsa"); got != filepath.Join("/home/fake", ".ssh", "id_rsa") {
		t.Fatalf("expanded: %q", got)
	}
	if got := expandJetBrainsPath("/keys/id_rsa"); got != "/keys/id_rsa" {
		t.Fatalf("plain path: %q", got)
	}
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if got := expandJetBrainsPath("$USER_HOME$/.ssh/id_rsa"); got != "$USER_HOME$/.ssh/id_rsa" {
		t.Fatalf("unknown home: %q", got)
	}
}

func TestWorkbenchImport(t *testing.T) {
	for _, path := range []string{fixture(t, "workbench"), fixture(t, "workbench", "connections.xml")} {
		preview, err := buildPreview(SourceWorkbench, path)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Rows) != 2 {
			t.Fatalf("rows: %d", len(preview.Rows))
		}
		crm := rowByName(t, preview.Rows, "CRM via bastion")
		if crm.DbType != "mysql" || crm.Host != "127.0.0.1" || crm.Database != "crm" || crm.Username != "crm_rw" || !crm.SSL || !crm.PasswordUnavailable {
			t.Fatalf("crm: %+v", crm)
		}
		if tunnel := crm.Tunnel; tunnel == nil || tunnel.Host != "jump.example.test" || tunnel.Port != 2201 || tunnel.Username != "tunnel" || tunnel.AuthType != "key" {
			t.Fatalf("crm tunnel: %+v", crm.Tunnel)
		}
		reporting := rowByName(t, preview.Rows, "Reporting")
		if reporting.Port != 3310 || reporting.Tunnel != nil || reporting.SSL {
			t.Fatalf("reporting: %+v", reporting)
		}
	}
}

func TestHeidiSQLImport(t *testing.T) {
	preview, err := buildPreview(SourceHeidiSQL, fixture(t, "heidisql", "settings.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rows) != 4 {
		t.Fatalf("rows: %+v", preview.Rows)
	}
	local := rowByName(t, preview.Rows, "Local MySQL")
	if local.DbType != "mysql" || local.password != "fake-heidi-pw" || local.Database != "app" || local.Port != 3306 {
		t.Fatalf("local: %+v", local)
	}
	pg := rowByName(t, preview.Rows, "PG tunnel")
	if pg.DbType != "postgres" || pg.Folder != "Team" || !pg.SSL || pg.Username != "pgadmin" {
		t.Fatalf("pg: %+v", pg)
	}
	if tunnel := pg.Tunnel; tunnel == nil || tunnel.Host != "ssh.example.test" || tunnel.Port != 2022 || tunnel.password != "fake-tunnel-pw" {
		t.Fatalf("pg tunnel: %+v", pg.Tunnel)
	}
	files := rowByName(t, preview.Rows, "Files")
	if files.DbType != "sqlite" || files.FilePath != `C:\fake\data.sqlite3` {
		t.Fatalf("files: %+v", files)
	}
	if rowByName(t, preview.Rows, "Legacy IB").Status != StatusUnsupported {
		t.Fatal("InterBase must be unsupported")
	}
	if _, ok := decodeHeidiPassword("zz1"); ok {
		t.Fatal("invalid obfuscation must fail")
	}
}

func TestHeidiSQLUndecodablePasswordAsksForOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heidi.txt")
	content := `Servers\Bad\Host<|||>1<|||>db.example.test
Servers\Bad\NetType<|||>3<|||>0
Servers\Bad\Password<|||>1<|||>zz1
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := buildPreview(SourceHeidiSQL, path)
	if err != nil {
		t.Fatal(err)
	}
	bad := rowByName(t, preview.Rows, "Bad")
	if bad.HasPassword || !bad.PasswordUnavailable || len(bad.Warnings) != 1 || bad.Warnings[0] != WarnPasswordUndecrypted {
		t.Fatalf("bad: %+v", bad)
	}
}

func TestMalformedAndBoundedInputs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		source, path, code string
	}{
		{SourceNavicat, write("bad.ncx", "<Connections><Connection"), "malformed"},
		{SourceDBeaver, write("data-sources.json", "{not json"), "malformed"},
		{SourceWorkbench, write("connections.xml", "<other/>"), "malformed"},
		{SourceHeidiSQL, write("heidi.txt", "just text"), "malformed"},
		{SourceDataGrip, write("dataSources.xml", "<project/>"), "malformed"},
		{SourceNavicat, filepath.Join(dir, "missing.ncx"), "not_found"},
		{SourceNavicat, "relative/path.ncx", "path_invalid"},
		{"unknown", dir, "source_invalid"},
	}
	for _, item := range cases {
		_, err := buildPreview(item.source, item.path)
		if err == nil || !strings.Contains(err.Error(), "db_import: "+item.code) {
			t.Fatalf("%s %s: want %s, got %v", item.source, item.path, item.code, err)
		}
	}
	large := filepath.Join(dir, "large.ncx")
	if err := os.WriteFile(large, bytes.Repeat([]byte(" "), maxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPreview(SourceNavicat, large); err == nil || !strings.Contains(err.Error(), "file_too_large") {
		t.Fatalf("large file: %v", err)
	}
	var many strings.Builder
	many.WriteString(`<Connections Ver="1.5">`)
	for i := 0; i < maxCandidates+5; i++ {
		many.WriteString(`<Connection ConnectionName="c" ConnType="MYSQL" Host="h"/>`)
	}
	many.WriteString(`</Connections>`)
	preview, err := buildPreview(SourceNavicat, write("many.ncx", many.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rows) != maxCandidates || len(preview.Warnings) != 1 || preview.Warnings[0] != WarnTruncated {
		t.Fatalf("rows=%d warnings=%v", len(preview.Rows), preview.Warnings)
	}
}

func TestJDBCURLParsing(t *testing.T) {
	cases := []struct {
		url      string
		kind     string
		host     string
		port     int
		database string
		file     string
		ssl      bool
	}{
		{"jdbc:mysql://a.test:3307/shop?useSSL=true", "mysql", "a.test", 3307, "shop", "", true},
		{"jdbc:mariadb://h1.test,h2.test:3306/db", "mariadb", "h1.test", 0, "db", "", false},
		{"jdbc:postgresql://[::1]:5432/pg", "postgres", "::1", 5432, "pg", "", false},
		{"jdbc:jtds:sqlserver://ms.test:1433/erp", "sqlserver", "ms.test", 1433, "erp", "", false},
		{"jdbc:oracle:thin:@ora.test:1521:ORCL", "oracle", "ora.test", 1521, "ORCL", "", false},
		{"jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=o.test)(PORT=1530))(CONNECT_DATA=(SERVICE_NAME=svc)))", "oracle", "o.test", 1530, "svc", "", false},
		{"jdbc:sqlite:/data/app.db", "sqlite", "", 0, "", "/data/app.db", false},
		{"jdbc:duckdb:/data/w.duckdb", "duckdb", "", 0, "", "/data/w.duckdb", false},
		{"jdbc:clickhouse://ch.test:8443/default?ssl=true", "clickhouse", "ch.test", 8443, "default", "", true},
		{"jdbc:neo4j:bolt://graph.test:7687", "neo4j", "graph.test", 7687, "", "", false},
		{"jdbc:exa:exa.test:8563;schema=S", "exasol", "exa.test", 8563, "S", "", false},
		{"jdbc:sap://hana.test:30015/", "saphana", "hana.test", 30015, "", "", false},
		{"jdbc:impala://imp.test:21050/default", "impala", "imp.test", 21050, "default", "", false},
		{"jdbc:cloudspanner:/projects/p/instances/i/databases/d", "spanner", "spanner.googleapis.com", 0, "projects/p/instances/i/databases/d", "", false},
		{"jdbc:phoenix:zk1.test,zk2.test:2181:/hbase", "hbase", "zk1.test", 2181, "", "", false},
		{"mongodb://m.test:27017/admin", "mongodb", "m.test", 27017, "admin", "", false},
	}
	for _, item := range cases {
		info, ok := parseJDBCURL(item.url)
		if !ok || info.Kind != item.kind || info.Host != item.host || info.Port != item.port || info.Database != item.database || info.FilePath != item.file || info.SSL != item.ssl {
			t.Errorf("%s: ok=%v %+v", item.url, ok, info)
		}
	}
	if _, ok := parseJDBCURL("jdbc:h2:mem:test"); ok {
		t.Error("h2 must not parse as a known shape")
	}
	if info, _ := parseJDBCURL("jdbc:postgresql://u:p%40ss@h.test/db"); info.User != "u" || info.Password != "p@ss" {
		t.Errorf("credentials in URL: %+v", info)
	}
}

func TestResolveKindAliases(t *testing.T) {
	cases := map[string]string{
		"mysql8": "mysql", "mariaDB": "mariadb", "postgres-jdbc": "postgres", "sqlite_jdbc": "sqlite",
		"sqlserver.ms": "sqlserver", "mssql_jdbc_ms_new": "sqlserver", "mysql.8": "mysql", "kingbase8": "kingbase",
		"apache_hive2": "", "hive2": "hive", "HANA": "saphana", "MONGODB": "mongodb", "generic": "", "db2": "",
		"dolt": "dolt", "GreatSQL": "greatsql", "OpenTenBase": "opentenbase", "tbase": "opentenbase",
	}
	for name, want := range cases {
		if got := resolveKind(name); got != want {
			t.Errorf("resolveKind(%q) = %q, want %q", name, got, want)
		}
	}
	// Fallback order: unknown driver, known provider.
	if got := resolveKind("custom_driver", "postgresql"); got != "postgres" {
		t.Errorf("provider fallback: %q", got)
	}
}

func TestDetectLocations(t *testing.T) {
	home := t.TempDir()
	mk := func(parts ...string) {
		path := filepath.Join(append([]string{home}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("Library", "DBeaverData", "workspace6", "General", ".dbeaver", "data-sources.json")
	mk("Library", "DBeaverData", "workspace6", "Empty", "readme.txt")
	mk("Library", "Application Support", "JetBrains", "DataGrip2024.2", "options", "dataSources.xml")
	mk("Library", "Application Support", "JetBrains", "Toolbox", "options", "dataSources.xml")
	mk("Library", "Application Support", "MySQL", "Workbench", "connections.xml")
	mk("DataGripProjects", "shop", ".idea", "dataSources.xml")
	found := detectLocations(locationEnv{goos: "darwin", home: home})
	sources := []string{}
	for _, location := range found {
		sources = append(sources, location.Source+":"+location.Label)
	}
	want := "dbeaver:DBeaver · General,datagrip:DataGrip2024.2,datagrip:DataGrip · shop,workbench:MySQL Workbench"
	if strings.Join(sources, ",") != want {
		t.Fatalf("detected %v", sources)
	}
	linuxRoot, _, linuxWorkbench := toolRoots(locationEnv{goos: "linux", home: "/h"})
	if linuxRoot != filepath.Join("/h", ".local", "share", "DBeaverData", "workspace6") || linuxWorkbench != filepath.Join("/h", ".mysql", "workbench") {
		t.Fatalf("linux roots: %s %s", linuxRoot, linuxWorkbench)
	}
	windowsRoot, jetbrains, _ := toolRoots(locationEnv{goos: "windows", home: "/h", appData: "/appdata"})
	if windowsRoot != filepath.Join("/appdata", "DBeaverData", "workspace6") || jetbrains != filepath.Join("/appdata", "JetBrains") {
		t.Fatalf("windows roots: %s %s", windowsRoot, jetbrains)
	}
}

type memoryStore struct {
	value []json.RawMessage
	fail  error
}

func useMemoryStore(t *testing.T, initial string) *memoryStore {
	t.Helper()
	store := &memoryStore{}
	if initial != "" {
		list, err := decodeStoredList([]byte(initial))
		if err != nil {
			t.Fatal(err)
		}
		store.value = list
	}
	originalUpdate, originalEncrypt := updateStoredConnections, encryptSecret
	updateStoredConnections = func(update func([]json.RawMessage) ([]json.RawMessage, error)) error {
		if store.fail != nil {
			return store.fail
		}
		next, err := update(append([]json.RawMessage(nil), store.value...))
		if err != nil {
			return err
		}
		store.value = next
		return nil
	}
	encryptSecret = func(plain string) (string, error) { return "enc:v2:" + hex.EncodeToString([]byte(plain)), nil }
	t.Cleanup(func() { updateStoredConnections, encryptSecret = originalUpdate, originalEncrypt })
	return store
}

func previewSession(t *testing.T, source, path string) Preview {
	t.Helper()
	preview, err := buildPreview(source, path)
	if err != nil {
		t.Fatal(err)
	}
	storeSession(preview)
	t.Cleanup(func() { dropSession(preview.ImportID) })
	return preview
}

func TestCommitStoresEncryptedConnections(t *testing.T) {
	store := useMemoryStore(t, `[{"id":"existing","name":"Keep me"}]`)
	preview := previewSession(t, SourceDBeaver, dbeaverWorkspace(t))
	mysql := rowByName(t, preview.Rows, "Orders MySQL")
	hive := rowByName(t, preview.Rows, "Warehouse")
	result, err := commitImport(commitArgs{
		ImportID:    preview.ImportID,
		Group:       "Imported",
		KeepFolders: true,
		Rows: []commitRow{
			{ID: mysql.ID},
			{ID: hive.ID, Driver: &DriverRef{PluginID: "bigdata-driver", DriverID: "hive"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || len(store.value) != 3 || string(store.value[0]) != `{"id":"existing","name":"Keep me"}` {
		t.Fatalf("result=%+v store=%d", result, len(store.value))
	}
	var first, second storedConnection
	if err := json.Unmarshal(store.value[1], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(store.value[2], &second); err != nil {
		t.Fatal(err)
	}
	if first.DbType != "mysql" || first.Group != "Imported/Prod" || !first.SavePassword || first.Driver != nil ||
		first.Password != "enc:v2:"+hex.EncodeToString([]byte("fake-orders-pw")) {
		t.Fatalf("first: %+v", first)
	}
	if first.Tunnel == nil || first.Tunnel.Password != "enc:v2:"+hex.EncodeToString([]byte("fake-jump-pw")) || first.Tunnel.AuthType != "password" {
		t.Fatalf("first tunnel: %+v", first.Tunnel)
	}
	if second.DbType != "plugin" || second.Driver == nil || second.Driver.DriverID != "hive" || second.SavePassword || second.Port != 10000 {
		t.Fatalf("second: %+v", second)
	}
	for _, raw := range store.value {
		if strings.Contains(string(raw), "fake-orders-pw") || strings.Contains(string(raw), "fake-jump-pw") {
			t.Fatal("plaintext secret persisted")
		}
	}
	if _, ok := claimSession(preview.ImportID); ok {
		t.Fatal("session must be consumed by a successful commit")
	}
}

func TestCommitValidation(t *testing.T) {
	store := useMemoryStore(t, "")
	preview := previewSession(t, SourceDBeaver, dbeaverWorkspace(t))
	hive := rowByName(t, preview.Rows, "Warehouse")
	db2 := rowByName(t, preview.Rows, "Mainframe")
	mysql := rowByName(t, preview.Rows, "Orders MySQL")
	cases := []struct {
		args commitArgs
		code string
	}{
		{commitArgs{ImportID: "missing", Rows: []commitRow{{ID: mysql.ID}}}, "session_expired"},
		{commitArgs{ImportID: preview.ImportID}, "selection_invalid"},
		{commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: "nope"}}}, "selection_invalid"},
		{commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: mysql.ID}, {ID: mysql.ID}}}, "selection_invalid"},
		{commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: hive.ID}}}, "driver_required"},
		{commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: hive.ID, Driver: &DriverRef{PluginID: "x", DriverID: "impala"}}}}, "driver_required"},
		{commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: db2.ID}}}, "unsupported_type"},
		{commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: mysql.ID, Driver: &DriverRef{PluginID: "../x", DriverID: "mysql"}}}}, "driver_invalid"},
		{commitArgs{ImportID: preview.ImportID, Group: strings.Repeat("g", maxGroupBytes+1), Rows: []commitRow{{ID: mysql.ID}}}, "group_invalid"},
	}
	for _, item := range cases {
		_, err := commitImport(item.args)
		if err == nil || !strings.Contains(err.Error(), "db_import: "+item.code) {
			t.Fatalf("want %s, got %v", item.code, err)
		}
	}
	if len(store.value) != 0 {
		t.Fatal("failed commits must not write")
	}
	store.fail = errors.New("disk full")
	if _, err := commitImport(commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: mysql.ID}}}); err == nil || strings.Contains(err.Error(), "fake-") {
		t.Fatalf("store failure: %v", err)
	}
	store.fail = nil
	// The session survives failures and can still be committed.
	if _, err := commitImport(commitArgs{ImportID: preview.ImportID, Rows: []commitRow{{ID: mysql.ID}}}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsExpireAndAreBounded(t *testing.T) {
	originalNow := now
	current := time.Unix(1_700_000_000, 0)
	now = func() time.Time { return current }
	t.Cleanup(func() { now = originalNow })
	for i := 0; i < maxSessions+2; i++ {
		storeSession(Preview{ImportID: "s" + string(rune('a'+i))})
		current = current.Add(time.Second)
	}
	sessionsMu.Lock()
	count := len(sessions)
	sessionsMu.Unlock()
	if count != maxSessions {
		t.Fatalf("sessions: %d", count)
	}
	current = current.Add(sessionTTL + time.Minute)
	if _, ok := claimSession("sf"); ok {
		t.Fatal("expired session must be gone")
	}
}

func TestBuildRecordKeepsTunnelOnlyForBuiltinDrivers(t *testing.T) {
	useMemoryStore(t, "")
	candidate := &Candidate{
		Name: "c", DbType: "clickhouse", Status: StatusReady, Host: "ch.example.test", Port: 8123,
		Tunnel: &Tunnel{Type: "ssh", Host: "jump.example.test", Port: 22, Username: "ops", AuthType: "password", password: "fake-jump-pw"},
	}
	withDriver, err := buildRecord(candidate, &DriverRef{PluginID: "example-plugin", DriverID: "clickhouse"}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if withDriver.Tunnel != nil || withDriver.SavePassword {
		t.Fatalf("plugin driver record: %+v", withDriver)
	}
	builtin, err := buildRecord(candidate, nil, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if builtin.Tunnel == nil || !builtin.SavePassword {
		t.Fatalf("built-in record: %+v", builtin)
	}
}

func TestTargetGroup(t *testing.T) {
	if got := targetGroup("", "Prod", true); got != "Prod" {
		t.Fatal(got)
	}
	if got := targetGroup("Imported", "Prod", false); got != "Imported" {
		t.Fatal(got)
	}
	if got := targetGroup("Imported", "", true); got != "Imported" {
		t.Fatal(got)
	}
}

func TestDecodeStoredListRejectsNonArrays(t *testing.T) {
	if _, err := decodeStoredList([]byte(`{"a":1}`)); err == nil {
		t.Fatal("object must be rejected")
	}
	if list, err := decodeStoredList([]byte("null")); err != nil || len(list) != 0 {
		t.Fatal("null means empty")
	}
}

func TestDriverKindsNeedPluginByDriverID(t *testing.T) {
	cases := []struct {
		name, kind string
		port       int
	}{
		{"hive2", "hive", 10000},
		{"spark", "sparksql", 10000},
		{"kyuubi", "kyuubi", 10009},
		{"transwarp", "inceptor", 10000},
		{"ArgoDB", "argodb", 10000},
		{"nebulagraph", "nebula", 9669},
	}
	for _, tc := range cases {
		c := &Candidate{}
		c.applyKind(resolveKind(tc.name))
		if c.Status != StatusNeedsPlugin || c.PluginDriverID != tc.kind || c.Port != tc.port || c.DbType != "" {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
	for kind := range driverKinds {
		if isBuiltinType(kind) || !driverIDPattern.MatchString(kind) {
			t.Errorf("driver kind %q is a built-in type or not a valid driver id", kind)
		}
	}
}
