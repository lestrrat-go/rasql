package web_test

import (
	"net/http"
	"testing"

	"github.com/lestrrat-go/rasql/sample/taskboard/internal/web"
)

func TestServerRunRejectsNilContext(t *testing.T) {
	controller, err := web.NewServer("127.0.0.1:0", http.NotFoundHandler()).Run(nil)
	if err == nil {
		t.Fatal("Run(nil) error = nil, want an error")
	}
	if controller != nil {
		t.Errorf("Run(nil) controller = %v, want nil", controller)
	}
}
