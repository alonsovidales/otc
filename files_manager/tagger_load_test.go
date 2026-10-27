// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/modelserver"
)

type loadedFake struct{}

func (loadedFake) Tags(context.Context, image.Image, imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	return []imagestagger.RAMTag{{Name: "beach", Score: 0.9}}, nil
}

func (loadedFake) TagsResized(context.Context, *image.RGBA, imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	return []imagestagger.RAMTag{{Name: "dog", Score: 0.8}}, nil
}

// A tagging model that fails to load no longer ends the process: the
// device goes on without tags (modelserver.ErrNoTagger, which processing
// skips quietly), says so in one Alert, and tries again with a growing
// pause until it loads.
func TestTaggerThatFailsToLoadIsTriedAgain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	expectAlert(mock, alerttext.EnglishOf(i18n.DevAlertTaggerNotLoaded()))

	rt := &retryTagger{}
	ready := make(chan struct{})
	tries := 0
	load := func() (loadedTagger, error) {
		tries++
		if tries < 4 {
			return nil, errors.New("open /opt/models/ram_plus_int8.onnx: no such file or directory")
		}
		return loadedFake{}, nil
	}
	var pauses []time.Duration
	loaded := make(chan struct{})
	gate := make(chan struct{})
	go func() {
		defer close(loaded)
		mg.loadTaggerRetrying(rt, load, ready, func(d time.Duration) {
			pauses = append(pauses, d)
			if len(pauses) == 1 {
				<-gate // the device serving meanwhile, without a model
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("processing kept waiting for a model that failed to load")
	}
	if _, err := rt.Tags(context.Background(), image.NewRGBA(image.Rect(0, 0, 1, 1)), imagestagger.RAMOptions{}); !errors.Is(err, modelserver.ErrNoTagger) {
		t.Fatalf("tags without a model: %v", err)
	}
	close(gate)
	<-loaded
	if tries != 4 {
		t.Fatalf("%d tries", tries)
	}
	if want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}; len(pauses) != 3 || pauses[0] != want[0] || pauses[1] != want[1] || pauses[2] != want[2] {
		t.Fatalf("pauses %v, want %v", pauses, want)
	}
	tags, err := rt.TagsResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 1, 1)), imagestagger.RAMOptions{})
	if err != nil || len(tags) != 1 || tags[0].Name != "dog" {
		t.Fatalf("tags once loaded: %v, %v", tags, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the alert, once: %v", err)
	}
}

// The pause between tries stops growing at an hour.
func TestTaggerRetryPauseStopsAtAnHour(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	expectAlert(mock, alerttext.EnglishOf(i18n.DevAlertTaggerNotLoaded()))
	mg := &Manager{dao: dao.NewWithDB(db)}
	tries := 0
	var last time.Duration
	mg.loadTaggerRetrying(&retryTagger{}, func() (loadedTagger, error) {
		if tries++; tries > 12 {
			return loadedFake{}, nil
		}
		return nil, errors.New("no")
	}, make(chan struct{}), func(d time.Duration) { last = d })
	if last != cTaggerRetryMax {
		t.Fatalf("last pause %v, want %v", last, cTaggerRetryMax)
	}
}
