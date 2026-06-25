package command

import (
	"fmt"
	"strings"
)

// HTTPVerbs are the generic passthrough verbs available on any http service.
var HTTPVerbs = map[string]string{
	"get":    "GET",
	"post":   "POST",
	"put":    "PUT",
	"patch":  "PATCH",
	"delete": "DELETE",
	"head":   "HEAD",
}

// IsVerb reports whether name is a generic verb (http or jsonrpc).
func IsVerb(name string) bool {
	if _, ok := HTTPVerbs[strings.ToLower(name)]; ok {
		return true
	}
	return strings.ToLower(name) == "call"
}

// Verb synthesizes an ephemeral Command from a generic verb and its args.
//
//	get  <path> [query]
//	post <path> [@file | inline-json]
//	call <method> [json-params]   (jsonrpc transports)
func Verb(transport, verb string, args []string) (*Command, error) {
	lower := strings.ToLower(verb)

	if lower == "call" {
		if len(args) < 1 {
			return nil, fmt.Errorf("usage: call <method> [json-params]")
		}
		params := "[]"
		if len(args) > 1 {
			params = args[1]
		}
		return &Command{
			ID:        "call",
			Method:    args[0],
			Params:    params,
			Write:     isWrite("jsonrpc-ws", args[0]),
			Synthetic: true,
		}, nil
	}

	method, ok := HTTPVerbs[lower]
	if !ok {
		return nil, fmt.Errorf("unknown verb %q", verb)
	}
	if len(args) < 1 {
		return nil, fmt.Errorf("usage: %s <path> [body|query]", lower)
	}
	path := args[0]
	query := ""
	// A path with an inline ?query splits into path + query.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		query = path[i+1:]
		path = path[:i]
	}
	cmd := &Command{
		ID:        lower,
		Method:    method,
		Path:      path,
		Query:     query,
		Write:     isWrite(transport, method),
		Synthetic: true,
	}
	if method != "GET" && method != "HEAD" && len(args) > 1 {
		cmd.Body = args[1]
	} else if method == "GET" && len(args) > 1 && query == "" {
		cmd.Query = args[1]
	}
	return cmd, nil
}
