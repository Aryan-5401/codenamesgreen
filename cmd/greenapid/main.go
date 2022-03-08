package main

import (
	"os"
	"net/http"
	"codenamesgreen/gameapi"
)

func main() {
	wordLists, err := gameapi.DefaultWordlists()
	if err != nil {
		panic(err)
	}
	h := gameapi.Handler(wordLists)
	port := os.Getenv("PORT")
	err = http.ListenAndServe(":"+port, h)
	panic(err)
}
