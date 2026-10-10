// Fixture: the device surface, a package whose errors reach people.
package websocket

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alonsovidales/otc/log"
)

var ErrUploadOnly = errors.New("this folder is upload only: nothing in it can be deleted")

type Resp struct {
	Error        bool
	ErrorMessage string `json:"error_message,omitempty"`
}

type alerter interface {
	AddErrorNotification(title, detail string) error
}

func handle(resp *Resp, dao alerter, name string, err error) {
	resp.Error, resp.ErrorMessage = true, "not available on this instance"
	resp.ErrorMessage = fmt.Sprintf("error trying to delete file: %s", err)
	_ = &Resp{ErrorMessage: "a collection needs a name"}
	log.Error("could not delete the file", err)
	fmt.Println("listening on the port")
	if strings.HasPrefix(name, "trash can items") {
		return
	}
	if name == "Some Name Here" {
		return
	}
	_ = dao.AddErrorNotification("This device left the bridge", "The bridge no longer knows it.")
	_ = fmt.Errorf("could not reach %s: %w", name, err)
	_ = fmt.Errorf("%s: %w", name, err)
	_ = errors.New("not found") // i18n-ignore: fixture for the escape hatch
	m := map[string]string{"error": "sign in first", "code": "login_required"}
	_ = m
	switch name {
	case "spaced out value":
	}
	_ = "SELECT name FROM files WHERE hash = ?"
}
