package db

import "testing"

func TestBackupFormatsSeparateRecoveryFromEvidence(t *testing.T) {
	for _, s := range []*dbSession{{kind: kindSQL, dialect: "mysql"}, {kind: kindRedis}, {kind: kindMongo}} {
		c := backupSessionCapability(s, s.dialect)
		if c.Version != 1 || len(c.Formats) == 0 {
			t.Fatalf("missing versioned format capabilities %+v", c)
		}
		for _, f := range c.Formats {
			if f.Verification != "unverified" {
				t.Fatal("adapter existence promoted to live evidence")
			}
			if f.Format == "sql" && f.Constrained {
				t.Fatal("arbitrary SQL advertised as constrained")
			}
		}
	}
}

func TestRedisCapabilityDisclosesManualEncodingFallback(t *testing.T) {
	c := backupSessionCapability(&dbSession{kind: kindRedis}, "redis")
	for _, limitation := range c.Limitations {
		if limitation == "redisRecoverySubset" {
			return
		}
	}
	t.Fatal("common Redis encoding manual fallback is not disclosed")
}
