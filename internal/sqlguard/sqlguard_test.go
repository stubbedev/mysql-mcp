package sqlguard

import "testing"

func TestReadOnlyClassification(t *testing.T) {
	cases := map[string]bool{
		"SELECT * FROM users":                   true,
		"select 1":                              true,
		"  SELECT 1  ":                          true,
		"SHOW TABLES":                           true,
		"SHOW DATABASES":                        true,
		"DESCRIBE users":                        true,
		"DESC users":                            true,
		"EXPLAIN SELECT * FROM users":           true,
		"SELECT a FROM t UNION SELECT b FROM u": true,
		"INSERT INTO t VALUES (1)":              false,
		"UPDATE t SET a=1":                      false,
		"DELETE FROM t":                         false,
		"DROP TABLE t":                          false,
		"CREATE TABLE t (id int)":               false,
		"TRUNCATE t":                            false,
	}
	for sql, want := range cases {
		got, err := ReadOnly(sql)
		if err != nil {
			t.Errorf("%q: unexpected error %v", sql, err)
			continue
		}
		if got != want {
			t.Errorf("%q: ReadOnly = %v, want %v", sql, got, want)
		}
	}
}

func TestReadOnlyRejectsStacked(t *testing.T) {
	if _, err := ReadOnly("SELECT 1; DROP TABLE t"); err == nil {
		t.Fatal("expected error for stacked statements")
	}
}

func TestEnsureReadOnly(t *testing.T) {
	if err := EnsureReadOnly("SELECT 1"); err != nil {
		t.Errorf("SELECT should pass: %v", err)
	}
	if err := EnsureReadOnly("DELETE FROM t"); err == nil {
		t.Error("DELETE should be rejected")
	}
}
