package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"caption-release-workbench/internal/release"
)

const defaultAddress = "127.0.0.1:19087"

type config struct {
	address   string
	database  string
	keyFile   string
	selfcheck bool
}

func parseConfig(arguments []string, portEnvironment string) (config, error) {
	flags := flag.NewFlagSet("caption-release-workbench", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configuration := config{}
	flags.StringVar(&configuration.address, "addr", "", "HTTP 监听地址")
	flags.StringVar(&configuration.database, "db", "caption-workbench.db", "SQLite 数据库路径")
	flags.StringVar(&configuration.keyFile, "key", "caption-workbench.key", "Ed25519 私钥文件")
	flags.BoolVar(&configuration.selfcheck, "selfcheck", false, "运行有界端到端自检后退出")
	if err := flags.Parse(arguments); err != nil {
		return configuration, fmt.Errorf("解析启动参数: %w", err)
	}
	if len(flags.Args()) != 0 {
		return configuration, errors.New("存在无法识别的位置参数")
	}
	if configuration.address == "" {
		configuration.address = defaultAddress
		if strings.TrimSpace(portEnvironment) != "" {
			port, err := strconv.Atoi(portEnvironment)
			if err != nil || port < 1024 || port > 65535 {
				return configuration, errors.New("PORT 必须是 1024 到 65535 之间的端口号")
			}
			configuration.address = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		}
	}
	if err := validateAddress(configuration.address); err != nil {
		return configuration, err
	}
	if strings.TrimSpace(configuration.database) == "" || strings.TrimSpace(configuration.keyFile) == "" {
		return configuration, errors.New("数据库和密钥路径不能为空")
	}
	return configuration, nil
}

func validateAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("监听地址必须为 host:port：%w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return errors.New("禁止缺失主机或使用通配监听地址")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("监听端口无效")
	}
	return nil
}

func loadCredentialService(path string) (*release.Service, error) {
	encoded, err := os.ReadFile(path)
	if err == nil {
		private, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(encoded)))
		if decodeErr != nil || len(private) != ed25519.PrivateKeySize {
			return nil, errors.New("签名私钥文件格式无效")
		}
		return release.New("local-ed25519-v1", ed25519.PrivateKey(private))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取签名私钥: %w", err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成签名私钥: %w", err)
	}
	if directory := filepath.Dir(path); directory != "." {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, fmt.Errorf("建立密钥目录: %w", err)
		}
	}
	data := []byte(base64.RawURLEncoding.EncodeToString(private) + "\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, fmt.Errorf("保存签名私钥: %w", err)
	}
	return release.New("local-ed25519-v1", private)
}
