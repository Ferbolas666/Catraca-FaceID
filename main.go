package main

import (
	"fmt"
	"net/http"

	"idface-sync/database"
	"idface-sync/handlers"
	"idface-sync/idface"

	syncer "idface-sync/sync"
)

func main() {

	err := database.Connect()
	if err != nil {
		panic(err)
	}

	// LOGIN ÚNICO DO SISTEMA
	session, err := idface.Login()
	if err != nil {
		panic(err)
	}

	fmt.Println("SESSION:", session)

	// PASSA SESSÃO PARA HANDLERS (sem login lá dentro)
	handlers.SetSession(session)

	// WORKER DO DEVICE (fila única já existente)
	idface.StartFaceWorker()
	handlers.StartWorker()

	// SYNC NÃO PODE BATER EM LOGIN
	go func() {
		err := syncer.SyncUsers(session)
		if err != nil {
			fmt.Println("ERRO SYNC:", err)
		}
	}()

	http.HandleFunc("/", handlers.Webhook)

	fmt.Println("SERVIDOR RODANDO EM :8080")

	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		panic(err)
	}
}
