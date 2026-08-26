// Package effects detects boundary-crossing calls: SQL, Redis, HTTP clients,
// and Kafka producers. Effect nodes are the anchors of a flow — the filter
// pipeline never drops them, and graph extraction does not descend past them
// into library internals.
package effects

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/ssax"
)

type Effect struct {
	Type      string // "sql" | "redis" | "http" | "kafka" | "grpc" | "security" | "storage"
	Detail    string // query text, command, or method name — best effort
	Topic     string // resolved Kafka topic, "" if not applicable/unresolved
	TopicExpr string // source-level hint when the topic is dynamic
}

// detector matches one library family. Matching happens on the callee's
// package path (major-version suffix normalized), receiver/interface type
// name, and method name — identical for static and interface calls, and
// satisfied equally by the real library or a test stub with the same shapes.
type detector struct {
	name    string
	matches func(info *ssax.CalleeInfo) bool
	build   func(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect
}

var detectors = []detector{
	{"sql-database-sql", matchDatabaseSQL, buildSQL},
	{"sql-pgx", matchPgx, buildSQL},
	{"sql-sqlx", matchSqlx, buildSQL},
	{"sql-gorm", matchGorm, buildORM},
	{"sql-xorm", matchXorm, buildORM},
	{"redis-go-redis", matchGoRedis, buildRedis},
	{"http-client", matchHTTPClient, buildHTTPClient},
	{"grpc-client", matchGRPCClient, buildGRPCClient},
	{"kafka-segmentio", matchSegmentioProducer, buildSegmentioProduce},
	{"kafka-sarama", matchSaramaProducer, buildSaramaProduce},
	{"kafka-confluent", matchConfluentProducer, buildConfluentProduce},
	{"security-passwords", matchPasswordHash, buildPasswordHash},
	{"security-jwt", matchJWTSign, buildJWTSign},
	{"storage-s3", matchObjectStorage, buildObjectStorage},
}

// --- security: password verification and token issuing ---

func matchPasswordHash(info *ssax.CalleeInfo) bool {
	switch ssax.NormalizePkg(info.Pkg) {
	case "golang.org/x/crypto/bcrypt":
		return strings.HasPrefix(info.Name, "CompareHash") || info.Name == "GenerateFromPassword"
	case "golang.org/x/crypto/argon2", "golang.org/x/crypto/scrypt":
		return info.Name == "IDKey" || info.Name == "Key"
	}
	return false
}

func buildPasswordHash(info *ssax.CalleeInfo, _ ssa.CallInstruction) *Effect {
	if strings.HasPrefix(info.Name, "CompareHash") {
		return &Effect{Type: "security", Detail: "verifies password"}
	}
	return &Effect{Type: "security", Detail: "hashes password"}
}

func matchJWTSign(info *ssax.CalleeInfo) bool {
	pkg := ssax.NormalizePkg(info.Pkg)
	if pkg != "github.com/golang-jwt/jwt" && pkg != "github.com/dgrijalva/jwt-go" {
		return false
	}
	return info.Name == "SignedString"
}

func buildJWTSign(info *ssax.CalleeInfo, _ ssa.CallInstruction) *Effect {
	return &Effect{Type: "security", Detail: "issues token"}
}

// --- object storage: S3 / MinIO ---

func matchObjectStorage(info *ssax.CalleeInfo) bool {
	pkg := ssax.NormalizePkg(info.Pkg)
	switch {
	case pkg == "github.com/minio/minio-go":
		return info.Type == "Client" && info.Name != ""
	case strings.HasPrefix(pkg, "github.com/aws/aws-sdk-go-v2/service/s3"),
		strings.HasPrefix(pkg, "github.com/aws/aws-sdk-go/service/s3"):
		return (info.Type == "Client" || info.Type == "S3" || info.Type == "PresignClient") && info.Name != ""
	}
	return false
}

func buildObjectStorage(info *ssax.CalleeInfo, _ ssa.CallInstruction) *Effect {
	return &Effect{Type: "storage", Detail: info.Name}
}

// Detect reports the effect a call site performs, or nil.
func Detect(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	if info == nil {
		return nil
	}
	for _, d := range detectors {
		if d.matches(info) {
			return d.build(info, site)
		}
	}
	return nil
}

// --- SQL ---

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func matchDatabaseSQL(info *ssax.CalleeInfo) bool {
	if info.Pkg != "database/sql" {
		return false
	}
	switch info.Type {
	case "DB", "Conn", "Tx", "Stmt":
		return hasAnyPrefix(info.Name, "Query", "Exec", "Prepare")
	}
	return false
}

func matchPgx(info *ssax.CalleeInfo) bool {
	pkg := ssax.NormalizePkg(info.Pkg)
	if pkg != "github.com/jackc/pgx" && !strings.HasPrefix(info.Pkg, "github.com/jackc/pgx/") {
		return false
	}
	switch info.Name {
	case "Query", "QueryRow", "Exec", "SendBatch", "Begin", "BeginTx", "CopyFrom":
		return true
	}
	return false
}

func matchSqlx(info *ssax.CalleeInfo) bool {
	if info.Pkg != "github.com/jmoiron/sqlx" {
		return false
	}
	return hasAnyPrefix(info.Name, "Get", "Select", "Query", "Exec", "NamedQuery", "NamedExec", "MustExec")
}

func matchGorm(info *ssax.CalleeInfo) bool {
	if info.Pkg != "gorm.io/gorm" || info.Type != "DB" {
		return false
	}
	switch info.Name {
	case "Find", "First", "Last", "Take", "Create", "Save", "Update", "Updates",
		"Delete", "Raw", "Exec", "Scan", "Count", "Pluck":
		return true
	}
	return false
}

// matchXorm matches xorm's terminal query methods (chainable builder
// methods like Where/Table are not effects; only execution is).
func matchXorm(info *ssax.CalleeInfo) bool {
	if !strings.HasPrefix(info.Pkg, "xorm.io/xorm") {
		return false
	}
	switch info.Type {
	case "Session", "Engine", "EngineGroup", "Interface", "EngineInterface":
	default:
		return false
	}
	switch info.Name {
	case "Insert", "InsertOne", "Update", "Delete", "Truncate", "Get", "Find",
		"FindAndCount", "Count", "Exist", "Exec", "Query", "QueryString",
		"QueryInterface", "Iterate", "Rows", "Sum", "SumInt", "Sums":
		return true
	}
	return false
}

// buildSQL captures the query text when the first string argument is a
// resolvable constant; otherwise the method name stands in.
func buildSQL(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	detail := info.Name
	for _, arg := range site.Common().Args[info.ArgOffset:] {
		if !isStringLike(arg) {
			continue
		}
		if sql, ok := ssax.ConstString(arg); ok {
			detail = compactQuery(sql)
		}
		break
	}
	return &Effect{Type: "sql", Detail: detail}
}

func buildORM(info *ssax.CalleeInfo, _ ssa.CallInstruction) *Effect {
	return &Effect{Type: "sql", Detail: info.Name}
}

func isStringLike(v ssa.Value) bool {
	basic, ok := v.Type().Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsString != 0
}

// compactQuery normalizes whitespace but keeps the query intact: the text
// is the data an analyst reads, so the UI may clamp it visually, but the
// document must carry it whole. A generous cap guards only against
// pathological megabyte literals.
func compactQuery(sql string) string {
	sql = strings.Join(strings.Fields(sql), " ")
	if len(sql) > 4000 {
		sql = sql[:3997] + "..."
	}
	return sql
}

// --- gRPC client ---

// matchGRPCClient: an outgoing RPC crosses a service boundary exactly like
// an HTTP client call. Generated client stubs funnel through
// (*grpc.ClientConn).Invoke / NewStream with the full method name as a
// string constant.
func matchGRPCClient(info *ssax.CalleeInfo) bool {
	return info.Pkg == "google.golang.org/grpc" && info.Type == "ClientConn" &&
		(info.Name == "Invoke" || info.Name == "NewStream")
}

func buildGRPCClient(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	e := &Effect{Type: "grpc", Detail: info.Name}
	for _, arg := range site.Common().Args[info.ArgOffset:] {
		if method, ok := ssax.ConstString(arg); ok && strings.HasPrefix(method, "/") {
			e.Detail = method
			break
		}
	}
	return e
}

// --- Redis ---

var redisNonCommands = map[string]bool{
	"Close": true, "Options": true, "AddHook": true, "String": true,
	"WithTimeout": true, "WithContext": true, "Context": true,
}

func matchGoRedis(info *ssax.CalleeInfo) bool {
	pkg := ssax.NormalizePkg(info.Pkg)
	if pkg != "github.com/redis/go-redis" && pkg != "github.com/go-redis/redis" {
		return false
	}
	switch info.Type {
	case "Client", "ClusterClient", "Ring", "Conn", "Tx", "Pipeline":
	default:
		return false
	}
	// The command surface is ~200 methods; package + client receiver +
	// exported method is more robust than an allowlist.
	if redisNonCommands[info.Name] || info.Name == "" {
		return false
	}
	return info.Name[0] >= 'A' && info.Name[0] <= 'Z'
}

func buildRedis(info *ssax.CalleeInfo, _ ssa.CallInstruction) *Effect {
	return &Effect{Type: "redis", Detail: info.Name}
}

// --- HTTP client ---

func matchHTTPClient(info *ssax.CalleeInfo) bool {
	if info.Pkg != "net/http" {
		return false
	}
	if info.Type == "Client" {
		switch info.Name {
		case "Do", "Get", "Post", "PostForm", "Head", "CloseIdleConnections":
			return info.Name != "CloseIdleConnections"
		}
		return false
	}
	if info.Type == "" {
		switch info.Name {
		case "Get", "Post", "PostForm", "Head",
			// Request construction is where the URL (or its template) is
			// still visible — usually inside the gateway that owns the
			// external service, before a generic transport sends it.
			"NewRequest", "NewRequestWithContext":
			return true
		}
	}
	return false
}

func buildHTTPClient(info *ssax.CalleeInfo, site ssa.CallInstruction) *Effect {
	args := site.Common().Args[info.ArgOffset:]
	if strings.HasPrefix(info.Name, "NewRequest") {
		method := "request"
		urlIdx := 1
		if info.Name == "NewRequestWithContext" {
			urlIdx = 2
		}
		if m, ok := ssax.ConstString(args[urlIdx-1]); ok {
			method = m
		}
		detail := method
		if urlIdx < len(args) {
			if u, ok := urlOrTemplate(args[urlIdx]); ok {
				detail = method + " " + u
			}
		}
		return &Effect{Type: "http", Detail: detail}
	}
	detail := info.Name
	for _, arg := range args {
		if url, ok := ssax.ConstString(arg); ok && strings.Contains(url, "://") {
			detail = info.Name + " " + url
			break
		}
	}
	return &Effect{Type: "http", Detail: detail}
}

// urlOrTemplate resolves a URL argument to a constant, or — the dominant
// gateway pattern — to the fmt.Sprintf format template that builds it
// ("%s/partner/%s"), which still names the endpoint.
func urlOrTemplate(v ssa.Value) (string, bool) {
	if u, ok := ssax.ConstString(v); ok {
		return u, true
	}
	if call, ok := v.(*ssa.Call); ok {
		if callee := call.Common().StaticCallee(); callee != nil &&
			callee.Name() == "Sprintf" && len(call.Common().Args) > 0 {
			if tpl, ok := ssax.ConstString(call.Common().Args[0]); ok {
				return tpl, true
			}
		}
	}
	return "", false
}
