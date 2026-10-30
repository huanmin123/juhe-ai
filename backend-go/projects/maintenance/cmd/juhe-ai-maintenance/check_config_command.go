// `maintenance check-config` 子命令（升级兼容与迁移账本设计 §12.3）：升级前
// 的「配置是否还合法」自检。只解析不连接、不启动；输出每个登记变量的
// name / source(default|env|alias) / valid / message。`--list --format
// markdown` 生成 env 契约表（docs 环境契约表的生成来源，§12.4）。
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/configregistry"
)

// ExitCheckFailed 是 check-config 的「存在缺失/非法/未知问题」退出码（exit 2，
// 与 maintenance 用法错误码同值但语义为检查结论）。
const ExitCheckFailed = 2

// runCheckConfigCommand 是 check-config 子命令入口，返回进程退出码。
func runCheckConfigCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance check-config", flag.ContinueOnError)
	envFile := fs.String("env-file", "", "按 KEY=VALUE 逐行解析的 env 文件（缺省读当前进程环境）")
	process := fs.String("process", "all", "检查视角：gateway | jobs | maintenance | all")
	list := fs.Bool("list", false, "输出 env 契约表（markdown）并退出")
	_ = fs.String("format", "text", "输出格式：text | markdown（--list 固定 markdown，text 预留）")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return ExitCheckFailed
	}
	if *list {
		fmt.Print(configregistry.MarkdownList())
		return 0
	}
	switch *process {
	case "gateway", "jobs", "maintenance", "all":
	default:
		fmt.Fprintf(os.Stderr, "--process 必须是 gateway|jobs|maintenance|all: %q\n", *process)
		return ExitCheckFailed
	}

	values := map[string]string{}
	if strings.TrimSpace(*envFile) != "" {
		file, err := os.Open(*envFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "打开 --env-file 失败: %v\n", err)
			return ExitCheckFailed
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq <= 0 {
				fmt.Fprintf(os.Stderr, "--env-file 第 %d 行不是 KEY=VALUE 形态: %q\n", lineNo, line)
				return ExitCheckFailed
			}
			key := strings.TrimSpace(line[:eq])
			value := strings.TrimSpace(line[eq+1:])
			values[key] = strings.Trim(value, `"'`)
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "读取 --env-file 失败: %v\n", err)
			return ExitCheckFailed
		}
	} else {
		for _, item := range os.Environ() {
			if eq := strings.IndexByte(item, '='); eq > 0 {
				values[item[:eq]] = item[eq+1:]
			}
		}
	}

	// 未知变量（env 文件/当前环境中存在但未登记的 JUHE_AI_*）。
	unknown := []string{}
	for key := range values {
		if strings.HasPrefix(key, "JUHE_AI_") && !configregistry.Known(key) {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)

	fmt.Println("== check-config ==")
	fmt.Printf("process=%s env=%s\n", *process, envFileDisplay(*envFile))
	failed := false
	for _, name := range configregistry.Names() {
		entry, _ := configregistry.Lookup(name)
		if *process != "all" && !processMatches(entry.Process, *process) {
			continue
		}
		source, value, aliasUsed := resolveSource(values, entry)
		message := ""
		valid := true
		if source == "default" && entry.Required {
			valid = false
			message = "必填缺失"
		} else if aliasUsed != "" {
			message = fmt.Sprintf("已弃用别名 %s，请改用 %s", aliasUsed, entry.Name)
		}
		if valid && entry.Kind != "" && source != "default" {
			if kindErr := validateKind(entry, value); kindErr != nil {
				valid = false
				message = kindErr.Error()
			}
		}
		if !valid {
			failed = true
		}
		display := value
		if entry.Sensitive && display != "" {
			display = "<redacted>"
		}
		fmt.Printf("%-60s source=%-7s valid=%-5v %s\n", name, source, valid, message)
		_ = display
	}
	if len(unknown) > 0 {
		failed = true
		fmt.Printf("未登记的 JUHE_AI_* 变量 %d 个（手工 shell 残留或已删除变量，不影响运行但建议清理）: %v\n",
			len(unknown), unknown)
	}
	if failed {
		return ExitCheckFailed
	}
	fmt.Println("结论：全部通过")
	return 0
}

func envFileDisplay(path string) string {
	if strings.TrimSpace(path) == "" {
		return "<当前进程环境>"
	}
	return path
}

func processMatches(processes []string, wanted string) bool {
	for _, process := range processes {
		if process == wanted {
			return true
		}
	}
	return false
}

// resolveSource 返回 (source, value, aliasUsed)：env > alias > default。
func resolveSource(values map[string]string, entry *configregistry.Entry) (string, string, string) {
	if value, ok := values[entry.Name]; ok {
		return "env", value, ""
	}
	for _, alias := range entry.Aliases {
		if value, ok := values[alias]; ok {
			return "alias", value, alias
		}
	}
	return "default", entry.Default, ""
}

// validateKind 按声明 Kind 校验值形态；Kind/Range 未声明的条目不校验。
func validateKind(entry *configregistry.Entry, value string) error {
	switch entry.Kind {
	case "int":
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err != nil {
			return fmt.Errorf("期望整数，得到 %q", value)
		}
	case "bool":
		lower := strings.ToLower(strings.TrimSpace(value))
		if lower != "true" && lower != "false" && lower != "1" && lower != "0" {
			return fmt.Errorf("期望布尔值，得到 %q", value)
		}
	case "duration":
		if _, err := time.ParseDuration(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("期望时长，得到 %q", value)
		}
	}
	// url/path/enum 与 Range 数值区间的精确校验随元数据补齐逐步启用（P3 基线
	// 只登记名称，Kind 大多为空，此处不校验）。
	return nil
}
