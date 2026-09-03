package scanbench

import (
	"fmt"
	"io"
	"strings"
)

// WriteMarkdown renders the human-readable report. It states the environment
// alongside every number and marks unmeasured values as "未测量" instead of
// printing a zero that would read as a measurement.
func WriteMarkdown(w io.Writer, report Report) error {
	var b strings.Builder
	b.WriteString("# 栖境扫描基准报告\n\n")
	fmt.Fprintf(&b, "生成时间：%s\n\n", report.GeneratedAt.Format("2006-01-02 15:04:05 -0700"))

	b.WriteString("## 环境\n\n")
	b.WriteString("| 项目 | 值 |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| 应用版本 | %s |\n", nonEmpty(report.AppVersion))
	fmt.Fprintf(&b, "| 构建修订 | %s |\n", nonEmpty(report.Revision))
	fmt.Fprintf(&b, "| Go 版本 | %s |\n", nonEmpty(report.GoVersion))
	fmt.Fprintf(&b, "| 操作系统 | %s %s (%s) |\n", report.System.OS, nonEmpty(report.System.OSVersion), report.GOOS)
	fmt.Fprintf(&b, "| 架构 | %s |\n", report.System.Arch)
	fmt.Fprintf(&b, "| CPU | %s（%d 逻辑核心） |\n", nonEmpty(report.System.CPUModel), report.System.LogicalCPUs)
	fmt.Fprintf(&b, "| 内存 | %.1f GiB |\n", report.System.TotalRAMGiB)
	fmt.Fprintf(&b, "| 目标卷 | %s |\n", nonEmpty(report.System.Volume))
	fmt.Fprintf(&b, "| 磁盘总线 | %s |\n", nonEmpty(report.System.DiskBusType))
	fmt.Fprintf(&b, "| 驱动器类型 | %s |\n\n", nonEmpty(report.System.DriveKind))

	b.WriteString("## 结果\n\n")
	b.WriteString("| 场景 | 文件数 | 字节 | 哈希 | 耗时(s) | 文件/秒 | MiB/秒 | 进程峰值内存(MiB) | 子进程 CPU(s) | 状态 |\n")
	b.WriteString("| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, result := range report.Results {
		status := result.Status
		if result.Failure != "" {
			status = "失败：" + result.Failure
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %.2f | %.0f | %.1f | %s | %s | %s |\n",
			result.Name, result.FixtureFiles, result.FixtureBytes, boolLabel(result.HashEnabled),
			result.Seconds, result.FilesPerSecond, result.MiBPerSecond,
			measured(result.PeakMemoryMeasured, fmt.Sprintf("%.1f", result.PeakWorkingSetMiB)),
			measured(result.ChildCPUMeasured, fmt.Sprintf("%.2f", result.ChildCPUSeconds)),
			nonEmpty(status))
	}

	b.WriteString("\n## 读数说明\n\n")
	b.WriteString("- 夹具在测量前刚刚写入，因此文件系统缓存是热的。冷缓存数字需要清空\n")
	b.WriteString("  Windows 文件缓存，产品不申请这一权限，所以本报告不给出冷缓存推断值。\n")
	b.WriteString("- 进程峰值内存是测量进程的 working set 高水位，覆盖整轮运行，\n")
	b.WriteString("  不是对单次扫描的归因。\n")
	b.WriteString("- 子进程 CPU 来自 Job Object 记账；进程内引擎没有子进程，标记为未测量。\n")
	b.WriteString("- 每个场景只运行一次，数字未做多轮平均，因此不代表方差。\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func nonEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "未知"
	}
	return value
}

func measured(ok bool, value string) string {
	if !ok {
		return "未测量"
	}
	return value
}

func boolLabel(value bool) string {
	if value {
		return "开"
	}
	return "关"
}
