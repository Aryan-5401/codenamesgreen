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
	port, err := os.Getenv("PORT")
    if err != nil {
        port = ":3000"
    } 
	err = http.ListenAndServe(port, h)
	panic(err)
}
