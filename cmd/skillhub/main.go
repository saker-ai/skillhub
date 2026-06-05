package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/saker-ai/skillhub"
	"github.com/saker-ai/skillhub/pkg/auth"
	"github.com/saker-ai/skillhub/pkg/cli"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"

	// Blank imports register all built-in store backends with the driver
	// registry (pkg/store). 独立二进制保持「全功能」语义——支持
	// cfg.Store.Backend == "git" / "s3" / "oss" 三种值。
	// 嵌入方按需选择子集即可减小依赖体积。
	_ "github.com/saker-ai/skillhub/pkg/store/git"
	_ "github.com/saker-ai/skillhub/pkg/store/oss"
	_ "github.com/saker-ai/skillhub/pkg/store/s3"
)

// newLogger 按运行模式构造默认 *slog.Logger。
//
//   - serve: JSON handler + Info,因为生产环境需要结构化字段进 ELK / Loki;
//   - 其他子命令 (admin / login / search 等): TextHandler + Info,因为 CLI
//     使用者直接读 stderr,JSON 反而干扰可读性。
//
// SKILLHUB_LOG_FORMAT=json|text 与 SKILLHUB_LOG_LEVEL=debug|info|warn|error
// 让运维 / debug 时无需改代码即可切换输出风格。
func newLogger(serveMode bool) *slog.Logger {
	level := slog.LevelInfo
	if v := os.Getenv("SKILLHUB_LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}

	useJSON := serveMode
	if v := os.Getenv("SKILLHUB_LOG_FORMAT"); v != "" {
		useJSON = strings.ToLower(v) == "json"
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if useJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func main() {
	if len(os.Args) < 2 {
		runServer()
		return
	}

	switch os.Args[1] {
	case "serve":
		runServer()
	case "admin":
		handleAdmin()
	case "login":
		cli.Login(os.Args[2:])
	case "whoami":
		cli.WhoAmI(os.Args[2:])
	case "search":
		cli.Search(os.Args[2:])
	case "list":
		cli.List(os.Args[2:])
	case "inspect":
		cli.Inspect(os.Args[2:])
	case "install":
		cli.Install(os.Args[2:])
	case "uninstall":
		cli.Uninstall(os.Args[2:])
	case "installed":
		cli.Installed(os.Args[2:])
	case "update":
		cli.Update(os.Args[2:])
	case "publish":
		cli.Publish(os.Args[2:])
	case "plugin", "plugins":
		cli.Plugins(os.Args[2:])
	case "team-token", "team-tokens":
		cli.TeamTokens(os.Args[2:])
	case "help", "--help", "-h":
		cli.PrintUsage()
	default:
		// Backward compatible: unknown subcommand starts the server
		runServer()
	}
}

func runServer() {
	logger := newLogger(true)
	slog.SetDefault(logger)

	configPath := "configs/skillhub.yaml"
	if v := os.Getenv("SKILLHUB_CONFIG"); v != "" {
		configPath = v
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// signal.NotifyContext 是 Go 1.16+ 的标准做法，替代手写 channel + goroutine。
	// ctx.Done() 触发后 Hub.Run 会调用底层 Shutdown，完成 graceful 退出。
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	hub, err := skillhub.New(ctx, skillhub.WithConfig(cfg), skillhub.WithLogger(logger))
	if err != nil {
		// 不能用 logger.Error+os.Exit 之前先手动 cancel：上面 defer cancel() 在 os.Exit
		// 路径不会跑，signal handler 不解除问题不大（进程立即退出），但 cancel() 让
		// 底层的 NotifyContext goroutine 正确清理 channel。
		cancel()
		logger.Error("failed to create hub", "err", err)
		os.Exit(1)
	}
	defer func() { _ = hub.Close() }()

	logger.Info("SkillHub starting", "host", cfg.Server.Host, "port", cfg.Server.Port)
	if err := hub.Run(ctx); err != nil && err != http.ErrServerClosed {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}

func handleAdmin() {
	logger := newLogger(false)

	if len(os.Args) < 3 {
		fmt.Println("Usage:")
		fmt.Println("  skillhub admin create-user --handle <handle> --role <role> [--password <password>]")
		fmt.Println("  skillhub admin create-token --user <handle> --label <label>")
		fmt.Println("  skillhub admin set-password --user <handle> --password <password>")
		os.Exit(1)
	}

	configPath := "configs/skillhub.yaml"
	if v := os.Getenv("SKILLHUB_CONFIG"); v != "" {
		configPath = v
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	db, err := repository.NewDB(cfg.Database)
	if err != nil {
		logger.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()

	switch os.Args[2] {
	case "create-user":
		handle := getFlag(os.Args[3:], "--handle")
		role := getFlag(os.Args[3:], "--role")
		password := getFlag(os.Args[3:], "--password")
		if handle == "" {
			logger.Error("--handle is required")
			os.Exit(1)
		}
		if role == "" {
			role = "admin"
		}

		userRepo := repository.NewUserRepo(db)
		user := &model.User{
			ID:     uuid.New(),
			Handle: handle,
			Role:   role,
		}
		if err := userRepo.Create(ctx, user); err != nil {
			logger.Error("failed to create user", "err", err)
			os.Exit(1)
		}

		if password != "" {
			tokenRepo := repository.NewTokenRepo(db)
			authSvc := auth.NewService(tokenRepo, userRepo)
			if err := authSvc.SetPassword(ctx, user.ID, password); err != nil {
				logger.Error("failed to set password", "err", err)
				os.Exit(1)
			}
			fmt.Printf("User created: %s (ID: %s, role: %s, password set)\n", handle, user.ID, role)
		} else {
			fmt.Printf("User created: %s (ID: %s, role: %s)\n", handle, user.ID, role)
		}

	case "create-token":
		userHandle := getFlag(os.Args[3:], "--user")
		label := getFlag(os.Args[3:], "--label")
		if userHandle == "" {
			logger.Error("--user is required")
			os.Exit(1)
		}
		if label == "" {
			label = "CLI"
		}

		userRepo := repository.NewUserRepo(db)
		tokenRepo := repository.NewTokenRepo(db)
		authSvc := auth.NewService(tokenRepo, userRepo)

		user, err := userRepo.GetByHandle(ctx, userHandle)
		if err != nil || user == nil {
			logger.Error("user not found", "handle", userHandle)
			os.Exit(1)
		}

		rawToken, _, err := authSvc.CreateToken(ctx, user.ID, label, "full", 0)
		if err != nil {
			logger.Error("failed to create token", "err", err)
			os.Exit(1)
		}
		fmt.Printf("Token created for %s:\n%s\n", userHandle, rawToken)

	case "set-password":
		userHandle := getFlag(os.Args[3:], "--user")
		password := getFlag(os.Args[3:], "--password")
		if userHandle == "" {
			logger.Error("--user is required")
			os.Exit(1)
		}
		if password == "" {
			logger.Error("--password is required")
			os.Exit(1)
		}

		userRepo := repository.NewUserRepo(db)
		tokenRepo := repository.NewTokenRepo(db)
		authSvc := auth.NewService(tokenRepo, userRepo)

		user, err := userRepo.GetByHandle(ctx, userHandle)
		if err != nil || user == nil {
			logger.Error("user not found", "handle", userHandle)
			os.Exit(1)
		}

		if err := authSvc.SetPassword(ctx, user.ID, password); err != nil {
			logger.Error("failed to set password", "err", err)
			os.Exit(1)
		}
		fmt.Printf("Password set for %s\n", userHandle)

	default:
		fmt.Printf("Unknown admin command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func getFlag(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
