package bgprocessor

import (
	"time"

	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/session"
)

const (
	TaskTypeFileReprocess = iota
	TaskTypeImageReprocess
)

type task struct {
	taskType      int
	parameters    map[string]*any
	requireEncKey bool
}

type BgProcessor struct {
	session      *session.Session
	tasks        chan task
	dao          *dao.Dao
	filesManager *filesmanager.Manager
}

func Init(dao *dao.Dao, filesManager *filesmanager.Manager) (bg *BgProcessor) {
	bg = &BgProcessor{
		dao:          dao,
		filesManager: filesManager,
		tasks:        make(chan task),
	}

	go bg.processQueue()

	return bg
}

func (bg *BgProcessor) SetSession(ses *session.Session) {
	bg.session = ses
}

func (bg *BgProcessor) processQueue() {
	for task := range bg.tasks {
		if task.requireEncKey && bg.session != nil {
			go func() {
				time.Sleep(time.Second)
				bg.tasks <- task
			}()

			continue
		}

		task.run()
	}
}

func (tsk *task) run() {
	switch tsk.taskType {
	case TaskTypeFileReprocess:
		break
	}

}
