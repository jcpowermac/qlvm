package main

import (
	"time"

	"github.com/jcpowermac/qlvm/internal/gui"
)

func main() {
	model := gui.NewAppModel(2*time.Second, 30*time.Second)
	gui.New(model)
}
