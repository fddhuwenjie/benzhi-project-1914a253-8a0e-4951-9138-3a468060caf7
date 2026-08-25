# BENZHI_README

## 项目说明
- 项目：benzhi-project-1914a253-8a0e-4951-9138-3a468060caf7
- 项目用途：展柜微环境异常处置台提供从传感器异常上报、风险分级、保管员分派、现场检查、专家复核到条件关闭的可追溯闭环服务。
- Go 工具链：`golang:1.22`
- 前端工具链：无

## 项目描述
- 项目名称：展柜微环境异常处置台
- 项目概述：面向博物馆预防性保护团队的微环境异常闭环服务：接收展柜温湿度异常，形成处置单，完成风险研判、现场检查、临时调控、专家复核并可追溯关闭。
- 核心工作流：传感器异常上报→风险分级与检查清单→分派保管员→现场记录和临时调控→专家复核整改→关闭处置单并生成追溯摘要
- 对外接口：HTTP JSON API，提供异常事件、处置单、检查记录和复核关闭接口；服务支持 -addr=127.0.0.1:<port> 或 PORT，默认监听 127.0.0.1:19081。

## 标准构建、运行和测试命令
进入容器后执行：
cd '/app' && GOTOOLCHAIN=local go build ./...
cd /app && GOTOOLCHAIN=local go run ./cmd/microclimate -self-check -addr=127.0.0.1:19081
cd '/app' && GOTOOLCHAIN=local go test ./...

## Docker 构建和进入容器
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-project-1914a253-8a0e-4951-9138-3a468060caf7-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-project-1914a253-8a0e-4951-9138-3a468060caf7-arm64 linux/arm64
docker run -it benzhi-project-1914a253-8a0e-4951-9138-3a468060caf7-amd64:latest

## 题目验证命令
1. 预期退出码 0：`go test ./...`
2. 预期退出码 0：`go run ./cmd/microclimate -self-check -addr=127.0.0.1:19081`
