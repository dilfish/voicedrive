package main

import (
	"fmt"

	"github.com/Study-of-slag-weng/voicedrive1/internal/confirm"
	"github.com/Study-of-slag-weng/voicedrive1/internal/groupclient"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/mcp"
)

// toolSpec 声明一个 group.* 工具的转发映射：call 闭包走 groupclient，
// argKey 是 clarify 消歧覆写的参数键（服务端追问的是哪个参数）。
type toolSpec struct {
	name   string
	desc   string
	props  []mcp.Property
	argKey string
	call   func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error)
}

// toolSpecs 与 01-voice-group §5.2 / 02-linx §6.2 的 10 个端点一一对应。
var toolSpecs = []toolSpec{
	{
		name:   "group.create",
		desc:   "建一个新群；name 为群名；用户未口述口令时勿填 passcode，由系统生成",
		props:  []mcp.Property{{Name: "name", Type: "string", Required: true}, {Name: "passcode", Type: "string"}},
		argKey: "name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Create(argString(args, "name"), argString(args, "passcode"))
		},
	},
	{
		name:   "group.join",
		desc:   "用口令加入群组，入群即可直接说话；passcode 为口令",
		props:  []mcp.Property{{Name: "passcode", Type: "string", Required: true}},
		argKey: "passcode",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Join(argString(args, "passcode"))
		},
	},
	{
		name:   "group.switch",
		desc:   "把我的对讲焦点切到某群；name 为群名，可模糊匹配",
		props:  []mcp.Property{{Name: "name", Type: "string", Required: true}},
		argKey: "name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Switch(argString(args, "name"))
		},
	},
	{
		name:   "group.mute",
		desc:   "静音或取消静音某群的收听；name 为群名，on 为 true 静音、false 取消",
		props:  []mcp.Property{{Name: "name", Type: "string", Required: true}, {Name: "on", Type: "bool", Required: true}},
		argKey: "name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Mute(argString(args, "name"), argBool(args, "on"))
		},
	},
	{
		name:  "group.list",
		desc:  "查询我加入的所有群与当前焦点群",
		props: nil,
		call: func(gc *groupclient.Client, _ mcp.Args) (groupclient.Result, error) {
			return gc.List()
		},
	},
	{
		name:   "group.leave",
		desc:   "退出某个群；name 为群名",
		props:  []mcp.Property{{Name: "name", Type: "string", Required: true}},
		argKey: "name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Leave(argString(args, "name"))
		},
	},
	{
		name:   "group.rename",
		desc:   "修改群名；name 为旧群名，new_name 为新群名",
		props:  []mcp.Property{{Name: "name", Type: "string", Required: true}, {Name: "new_name", Type: "string", Required: true}},
		argKey: "name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Rename(argString(args, "name"), argString(args, "new_name"))
		},
	},
	{
		name:   "group.pat",
		desc:   "拍一拍某人以提醒对方（对方决定是否切群）；alias 为对方别名",
		props:  []mcp.Property{{Name: "alias", Type: "string", Required: true}},
		argKey: "alias",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Pat(argString(args, "alias"))
		},
	},
	{
		name:   "group.invite",
		desc:   "邀请好友加入群（对方须确认）；alias 为好友别名，group_name 为目标群名",
		props:  []mcp.Property{{Name: "alias", Type: "string", Required: true}, {Name: "group_name", Type: "string", Required: true}},
		argKey: "group_name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.Invite(argString(args, "alias"), argString(args, "group_name"))
		},
	},
	{
		name:   "group.query_member",
		desc:   "查询某群里有没有某人；group_name 为群名，alias 为成员别名",
		props:  []mcp.Property{{Name: "group_name", Type: "string", Required: true}, {Name: "alias", Type: "string", Required: true}},
		argKey: "group_name",
		call: func(gc *groupclient.Client, args mcp.Args) (groupclient.Result, error) {
			return gc.QueryMember(argString(args, "group_name"), argString(args, "alias"))
		},
	},
}

// registerGroupTools 按 toolSpecs 注册全部 group.* 工具（02 §6.2）。
// preSwitch 是 group.switch 切群前截断对讲在途流的钩子（03 §6.1 预留，可为 nil）。
func registerGroupTools(reg *mcp.Registry, gc *groupclient.Client, cf *confirm.Machine, preSwitch func(), logger logx.Logger) error {
	for _, spec := range toolSpecs {
		spec := spec
		tool := mcp.Tool{
			Name:        spec.name,
			Description: spec.desc,
			Properties:  spec.props,
			Handler: func(args mcp.Args) string {
				if spec.name == "group.switch" && preSwitch != nil {
					preSwitch()
				}
				return dispatchToVD(spec, gc, cf, args, logger)
			},
		}
		if err := reg.Register(tool); err != nil {
			return fmt.Errorf("register %s: %w", spec.name, err)
		}
	}
	return nil
}

// dispatchToVD 转发 voicedrive 并落确认语境：clarify 反问进入 Pending，
// 话术原样返回（含 error 业务话术，让 LLM/TTS 直接可读，02 §6.3/§6.4）。
func dispatchToVD(spec toolSpec, gc *groupclient.Client, cf *confirm.Machine, args mcp.Args, logger logx.Logger) string {
	res, err := spec.call(gc, args)
	if err != nil {
		logger.Warnf("%s: %v", spec.name, err)
		return groupclient.NetworkErrorPhrase
	}
	if res.Status == groupclient.StatusClarify && cf != nil {
		if aerr := cf.Ask(confirm.Pending{
			Question: res.Message,
			Options:  res.Options,
			Tool:     spec.name,
			Args:     args,
			ArgKey:   spec.argKey,
		}); aerr != nil {
			logger.Warnf("%s 进入确认语境失败: %v", spec.name, aerr)
		}
	}
	return res.Message
}

// argString 取 string 参数；缺失或类型不符返回空串（schema 已校验类型）。
func argString(args mcp.Args, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// argBool 取 bool 参数；缺失返回 false（schema 已校验类型）。
func argBool(args mcp.Args, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}
