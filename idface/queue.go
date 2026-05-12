package idface

import "sync"

var faceQueue = make(chan func(), 1000)

var once sync.Once

func StartFaceWorker() {
	once.Do(func() {
		go func() {
			for job := range faceQueue {
				job()
			}
		}()
	})
}

func Enqueue(job func()) {
	faceQueue <- job
}
