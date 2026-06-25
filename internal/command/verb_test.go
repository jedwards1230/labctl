package command

import "testing"

func TestVerbGet(t *testing.T) {
	c, err := Verb("http", "get", []string{"/api/v3/movie?monitored=true"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != "GET" || c.Path != "/api/v3/movie" || c.Query != "monitored=true" {
		t.Fatalf("got %+v", c)
	}
	if c.Write {
		t.Error("GET should not be a write")
	}
}

func TestVerbPostBody(t *testing.T) {
	c, err := Verb("http", "post", []string{"/cruddb", `{"data":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != "POST" || c.Body != `{"data":1}` || !c.Write {
		t.Fatalf("got %+v", c)
	}
}

func TestVerbCall(t *testing.T) {
	c, err := Verb("jsonrpc-ws", "call", []string{"pool.query"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != "pool.query" || c.Params != "[]" {
		t.Fatalf("got %+v", c)
	}
	if c.Write {
		t.Error("pool.query should be a read")
	}
}

func TestVerbUsageErrors(t *testing.T) {
	if _, err := Verb("http", "get", nil); err == nil {
		t.Error("expected usage error for get with no path")
	}
	if _, err := Verb("http", "bogus", []string{"/x"}); err == nil {
		t.Error("expected error for unknown verb")
	}
}
