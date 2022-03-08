package main

import (
	"net/http"

	"codenamesgreen/gameapi"
)

func main() {
	wordLists, err := gameapi.DefaultWordlists()
	if err != nil {
		panic(err)
	}

	h := gameapi.Handler(wordLists)
	err = http.ListenAndServe(":0", h)
	panic(err)
}
