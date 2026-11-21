package db

import (
	"testing"

	"mincode/app/backend/db/sqlsafety"
)

// Cypher line comments end at CR too (openCypher: '//' ~[\r\n]*), so a write
// after a CR-terminated comment must not be classified as a read.
func TestCypherLineCommentEndsAtCarriageReturn(t *testing.T) {
	if kind := cypherKind("MATCH (n) RETURN n //x\rDETACH DELETE n"); kind == sqlsafety.KindRead {
		t.Fatalf("CR-terminated comment hid a DELETE: kind %v", kind)
	}
	if kind := cypherKind("MATCH (n) RETURN n // DETACH DELETE n"); kind != sqlsafety.KindRead {
		t.Fatalf("comment text must stay a comment: kind %v", kind)
	}
}
