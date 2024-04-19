package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	retryablehttp "github.com/hashicorp/go-retryablehttp"
	"github.com/xanzy/go-gitlab"
)

type App struct {
	ctx     context.Context
	gc      *gitlab.Client
	errChan chan error
	logger  *slog.Logger
	pid     int
	timeout time.Duration
	wg      sync.WaitGroup
}

func main() {
	app := NewApp()
	if len(os.Args) != 2 {
		app.logger.Info("missing project path arg")
		return
	}
	projectPath := os.Args[1]

	app.GetProjectIdByPath(projectPath)
	repIds := app.ListProjectRegistryRepositories(app.pid)

	repCount := len(repIds)
	if repCount == 0 {
		app.logger.Info("no registry repositories")
		return
	}

	var err error
	go app.DeleteRegistryRepositoriesChannel(repIds)
	err = <-app.errChan
	if err != nil {
		app.logger.Error(err.Error())
		os.Exit(1)
	}

	// app.wg.Add(repCount)
	// go app.DeleteRegistryRepositoriesWaitGroup(repIds)
	// app.wg.Wait()

	ctx, cancel := context.WithTimeout(app.ctx, app.timeout)
	defer cancel()
	err = app.WaitForRegistryRepositoriesDeletion(ctx, app.pid)
	if err != nil {
		app.logger.Error(err.Error())
		os.Exit(1)
	}

	app.logger.Info("registry repositories deleted")
}

func NewApp() *App {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := gitlab.NewClient(
		os.Getenv("GITLAB_API_TOKEN"),
		gitlab.WithBaseURL(os.Getenv("GITLAB_BASE_URL")),
	)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	return &App{
		ctx:     ctx,
		errChan: make(chan error),
		gc:      client,
		logger:  logger,
		timeout: time.Minute*5,
	}
}

func RetryHttpReq (*retryablehttp.Request) error {
	return nil
}

func (app *App) DeleteRegistryRepository(pid, rid int, fn func(*retryablehttp.Request) error) error {
	_, err := app.gc.ContainerRegistry.DeleteRegistryRepository(pid, rid, fn)
	if err != nil {
		return err
	}
	return nil
}

func (app *App) DeleteRegistryRepositoriesChannel(repIds []int) {
	app.logger.Info("delete registry repositories with channel method")
	errs := []error{}
	for _, id := range repIds {
		err := app.DeleteRegistryRepository(app.pid, id, RetryHttpReq)
		if err != nil {
			errs = append(errs, fmt.Errorf("unable to delete registry repository id `%d` Err: %v", id, err))
		}
	}
	app.errChan <-errors.Join(errs...)
}

func (app *App) DeleteRegistryRepositoriesWaitGroup(repIds []int) {
	app.logger.Info("delete registry repositories with waitgroup method")
	for _, id := range repIds {
		defer app.wg.Done()
		err := app.DeleteRegistryRepository(app.pid, id, RetryHttpReq)
		if err != nil {
			app.logger.Error(err.Error())
			os.Exit(1)
		}
	}
}

func (app *App) GetProjectIdByPath(path string) (int, error) {
	project, _, err := app.gc.Projects.GetProject(path, &gitlab.GetProjectOptions{})
	if err != nil {
		return 0, err
	}
	app.pid = project.ID
	return project.ID, nil
}

func (app *App) ListProjectRegistryRepositories(pid int) []int {
	tags, _, err := app.gc.ContainerRegistry.ListProjectRegistryRepositories(pid, &gitlab.ListRegistryRepositoriesOptions{})
	if err != nil {
		app.logger.Error(err.Error())
		os.Exit(1)
	}
	var arr []int
	for _, t := range tags {
		arr = append(arr, t.ID)
	}
	return arr
}

func (app *App) WaitForRegistryRepositoriesDeletion(ctx context.Context, pid int) error {
	app.logger.Info("waiting for deletion...")
	deletionComplete := false
	ticker := time.NewTicker(time.Second*5)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			repIds := app.ListProjectRegistryRepositories(pid)
			if len(repIds) == 0 {
				deletionComplete = true
			}
		case <-ctx.Done():
			return fmt.Errorf("registry repositories failed to delete within the allowed timeout: %v", app.timeout)
		}
		if deletionComplete {
			break
		}
	}
	return nil
}
