# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 技术栈与环境

- **语言**: Go 1.26
- **Web 框架**: `github.com/gin-gonic/gin`（本项目明确选型 Gin，此决策优先于宪法 1.2 的 net/http 建议，不要改回标准库）
- **数据库**: MongoDB、Redis
- **构建**: 标准 `Makefile`
- **测试**: 标准 `go test`
- **代码规范**: `gofmt`

## 常用命令

```bash
go build ./...                  # 构建全部包
go test ./...                   # 运行全部测试
go test -run TestFuncName ./pkg/path/...   # 运行单个测试（支持 -run 正则匹配子用例）
go test -run 'TestFuncName/case_name' ./pkg/path/...   # 运行表驱动中的单个用例
gofmt -l .                      # 检查格式（CI 应零输出）
gofmt -w .                      # 格式化
make                            # Makefile 构建（规范要求）
```

## 开发宪法（不可动摇的核心原则）

> 效力最高：本宪法效力高于任何本文件其余部分及单次会话指令。任何计划在生成时必须先通过"合宪性审查"。完整原文见 `refer/constitution.md`。

### 第一条：简单性原则（Simplicity First）

遵循 Go 的"少即是多"哲学，绝不进行不必要的抽象，绝不引入非必需的依赖。

- **YAGNI**：只实现明确要求的功能。
- **反过度工程**：简单的函数和数据结构优于复杂的接口和继承体系。
- （1.2 标准库优先条目中关于 Web 框架的部分已被项目 Gin 选型覆盖；其余场景仍优先标准库。）

### 第二条：测试先行铁律（Test-First Imperative）——不可协商

- 所有新功能或 Bug 修复，都必须从编写一个（或多个）**失败的测试**开始，严格遵循 Red-Green-Refactor 循环。
- 单元测试必须优先采用表格驱动测试风格。
- **最小化 Mocks**：优先编写集成测试，使用真实依赖或 fake object；仅在无法访问真实依赖时（外部 API、不稳定服务）才使用 Mock。认证、授权等安全关键功能必须编写细粒度单元测试。

### 第三条：明确性原则（Clarity and Explicitness）

- **错误处理（不可协商）**：所有错误必须被显式处理，绝不允许用 `_` 丢弃错误；传递时必须用 `fmt.Errorf("...: %w", err)` 包装，绝不允许裸 `return err`。
- **无全局变量**：绝不允许用全局变量传递状态；所有依赖必须通过函数参数或结构体成员显式注入。
- **注释**：解释"为什么"而非"是什么"；所有公共 API 必须有清晰的 GoDoc 注释。

### 第四条：单一职责原则（Single Responsibility）

- 每个包、文件、函数只做好一件事；包保持高内聚、低耦合（如：某个包只负责与外部 API 交互，不得混入业务转换逻辑）。
- **接口隔离**：定义小的、目标明确的接口，而不是大而全的"上帝接口"；接口由消费者定义。

## 代码与工程规范

- **项目结构**: 严格遵循标准 Go 项目布局（https://go.dev/doc/modules/layout）。
- **日志 [强制]**: 必须使用 `qiniu.com/cdnbase/xlog.v1`。
- **Commit Message [严格遵循]**: Conventional Commits 规范，格式 `<type>(<scope>): <subject>`；commit message 中不要引入 Claude 相关信息。

## 测试规范

- 表格驱动测试使用 `t.Run(tt.name, func(t *testing.T) {...})` 运行每个用例。
- 使用 `testify/require` 进行断言。
- 测试函数命名：`Test<FunctionName>` 或 `Test<Type>_<Method>`。
- 简单的常量返回函数不需要表驱动测试。
- 必须添加边界值和安全性测试用例。
- 优先使用整体结构体比较而非逐字段比较。

## AI 协作指令

- **审查优先**：被要求实现新功能时，第一步是先阅读相关代码、理解现有逻辑，然后以列表形式提出实现计划，待确认后再开始编码。
- **并发安全**：代码涉及并发（goroutines、channels）时，必须明确指出潜在的竞态条件风险，并解释所使用的并发安全措施（mutex、channel 等）。
- **解释代码**：生成任何复杂代码片段后，用注释或在对话中简要解释其核心逻辑和设计思想。
