package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"caption-release-workbench/internal/repository"
	"caption-release-workbench/internal/validator"
	"caption-release-workbench/internal/web"
	"caption-release-workbench/internal/workflow"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("字幕发布工作台启动失败：%v", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	configuration, err := parseConfig(arguments, os.Getenv("PORT"))
	if err != nil {
		return err
	}
	if configuration.selfcheck {
		return runSelfcheck(configuration.address)
	}
	store, err := repository.Open(configuration.database)
	if err != nil {
		return err
	}
	defer store.Close()
	credentials, err := loadCredentialService(configuration.keyFile)
	if err != nil {
		return err
	}
	application := workflow.New(store, validator.New(), credentials)
	server := &http.Server{
		Addr:              configuration.address,
		Handler:           web.New(application),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", configuration.address)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", configuration.address, err)
	}
	log.Printf("字幕发布工作台已监听 http://%s", listener.Addr())
	serveErrors := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
			return
		}
		serveErrors <- nil
	}()
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErrors:
		return err
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("关闭 HTTP 服务: %w", err)
		}
		return <-serveErrors
	}
}
