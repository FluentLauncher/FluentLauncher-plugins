//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

type handler struct{ desktop *desktop }

type menuEntry struct{ id, title, subtitle string }

var entries = []menuEntry{
	{"region", "区域截图", "拖选并调整 · Enter 复制 / Ctrl+S 保存 / P 贴图 / Esc 取消"},
	{"region-pin", "区域截图并原地贴图", "确认后原位覆盖 · 默认阴影 · 点击后方向键微调"},
	{"screen", "当前显示器截图", "复制鼠标所在显示器的画面，默认包含已有贴图"},
	{"all", "所有显示器截图", "复制整个虚拟桌面，默认包含已有贴图"},
	{"quick-save", "所有显示器截图并快速保存", "保存到系统图片文件夹下的 FluentLauncher"},
	{"folder", "打开截图保存目录", "打开系统图片文件夹下的 FluentLauncher"},
	{"show", "显示全部贴图", "恢复之前隐藏的图片"},
	{"hide", "隐藏全部贴图", "仅隐藏，图片继续保留在内存"},
	{"close", "关闭全部贴图", "释放所有贴图，不保留历史"},
	{"settings", "截图与贴图设置", "再次截图包含贴图、新贴图阴影；配置自动保存"},
}

func (h handler) Query(ctx context.Context, q pluginsdk.Query) ([]pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filter := strings.ToLower(strings.TrimSpace(q.Text))
	var result []pluginsdk.Result
	for _, entry := range entries {
		if filter != "" && !strings.Contains(strings.ToLower(entry.title+" "+entry.id+" "+entry.subtitle), filter) {
			continue
		}
		result = append(result, pluginsdk.Result{ID: entry.id, Title: entry.title, Subtitle: entry.subtitle,
			Actions: []pluginsdk.Action{{ID: entry.id, Title: entry.title, Kind: pluginsdk.KindInvoke}}})
	}
	if q.MaxResults > 0 && len(result) > q.MaxResults {
		result = result[:q.MaxResults]
	}
	return result, nil
}

func (h handler) Execute(ctx context.Context, req pluginsdk.ExecuteRequest) error {
	if req.ResultID != req.ActionID {
		return errors.New("截图动作与结果不匹配")
	}
	return h.execute(ctx, req.ActionID)
}

func (h handler) ExecuteCommand(ctx context.Context, req pluginsdk.ExecuteCommandRequest) error {
	return h.execute(ctx, req.CommandID)
}

func (h handler) HasActivity(context.Context) (bool, error) {
	return h.desktop.active.Load() != 0 || h.desktop.capturing.Load(), nil
}

func (h handler) execute(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	valid := false
	for _, entry := range entries {
		if entry.id == id {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("未知截图命令：%s", id)
	}
	if id == "region" || id == "region-pin" || id == "screen" || id == "all" || id == "quick-save" {
		if !h.desktop.capturing.CompareAndSwap(false, true) {
			return errors.New("已有截图正在进行，请先完成或取消")
		}
		if err := h.desktop.post(func() { h.desktop.startCapture(id) }); err != nil {
			h.desktop.capturing.Store(false)
			return err
		}
		return nil
	}
	return h.desktop.post(func() { h.desktop.command(id) })
}

func main() {
	runtime.LockOSThread()
	d, err := newDesktop()
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := pluginsdk.ServeFromManifest(handler{d}); err != nil {
			log.Print(err)
		}
		_ = d.post(d.shutdown)
	}()
	d.run()
}
