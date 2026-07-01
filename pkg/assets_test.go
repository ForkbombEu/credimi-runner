package assets

import "testing"

func TestReadFile(t *testing.T) {
	data, err := ReadFile("server/docs/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("embedded docs index is empty")
	}

	if _, err := ReadFile("missing.file"); err == nil {
		t.Fatal("expected missing embedded file error")
	}
}
