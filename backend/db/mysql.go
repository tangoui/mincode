// MySQL 系连接（go-sql-driver/mysql）。
//
// go-sql-driver/mysql 必须保持在 v1.7.1：v1.8.0 起驱动默认在握手包中发送
// connection attributes，而旧版 Apache Doris FE 会错误解析属性总长度并主动断开连接。
// 升级驱动前必须同时验证仍受支持的旧版 Doris 连接。
package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// mysqlDialTimeout 限制 TCP 建连；mysqlConnectTimeout 限制建连 + TLS + 认证握手的
// 整体耗时（通过 PingContext 的 deadline 传给驱动的 watchCancel）。不设置驱动的
// ReadTimeout/WriteTimeout：它们按单次 I/O 生效，会误杀正常的长查询。
var (
	mysqlDialTimeout    = 10 * time.Second
	mysqlConnectTimeout = 20 * time.Second
)

// newMySQLConfig 把前端连接配置转换为驱动配置。
//
// 勾选 SSL 时必须做完整的证书与主机名校验，且不允许回退到明文：
// 驱动内置的 "preferred" 模式等价于 InsecureSkipVerify + 允许明文回退，
// 主动中间人可以直接降级或冒充服务器拿到账号密码与查询数据。
func newMySQLConfig(cfg *DbConnectionConfig) *mysql.Config {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 3306
	}

	mc := mysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = fmt.Sprintf("%s:%d", host, port)
	mc.User = derefOr(cfg.Username, "")
	mc.Passwd = derefOr(cfg.Password, "")
	if d := trimmedOptional(cfg.Database); d != nil {
		mc.DBName = *d
	}
	mc.ParseTime = true
	mc.AllowNativePasswords = true
	mc.Params = map[string]string{"charset": "utf8mb4"}
	// interpolateParams=true 让驱动在客户端把 ? 参数插值为字面量，避免走
	// COM_STMT_PREPARE 预处理协议。StarRocks/Doris 等 MySQL 协议兼容库不支持
	// 预处理语句（Error 1064: Unsupported command(COM_STMT_PREPARE)），
	// 开启后即可正常执行带占位符的 schema/列/表操作查询。对原生 MySQL 亦安全。
	mc.InterpolateParams = true
	mc.Timeout = mysqlDialTimeout
	if cfg.SSL {
		mc.TLS = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: strings.Trim(host, "[]"),
		}
		mc.AllowFallbackToPlaintext = false
	}
	return mc
}

func buildMySQLSession(cfg *DbConnectionConfig) (*dbSession, error) {
	mc := newMySQLConfig(cfg)
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, fmt.Errorf("MySQL 配置无效: %w", err)
	}
	db := sql.OpenDB(connector)
	ctx, cancel := context.WithTimeout(context.Background(), mysqlConnectTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("MySQL 连接超时（%s 内未完成握手）: %w", mysqlConnectTimeout, err)
		}
		if cfg.SSL && isTLSVerificationError(err) {
			return nil, fmt.Errorf("MySQL SSL 证书校验失败（服务器证书不受信任或主机名不匹配；自签名证书需先加入系统信任库）: %w", err)
		}
		return nil, fmt.Errorf("MySQL 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "mysql",
		defaultDatabase: mc.DBName,
	}, nil
}

func isTLSVerificationError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &verify)
}
