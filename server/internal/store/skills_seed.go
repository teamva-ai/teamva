package store

import (
	"context"
	"fmt"
	"log"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// builtinSkillSeed 定义内置 Skills 商店条目。每个条目对应一个 GitHub 仓库
// （一个仓库一个商店卡片），包含仓库中全部 SKILL.md / references / assets /
// scripts / hooks（完整安装原仓库能力）。
//
// 更新策略：服务端定期检查仓库新 commit → 展示差异与可执行文件变化 →
// 用户确认 → 创建新的 SkillPackageVersion（不可变制品）。当前由发布
// 流程生成制品并调用 EnsureBuiltinSkillPackage 落库。
type builtinSkillSeed struct {
	slug        string
	name        string
	description string
	sourceURL   string
	license     string
	// 仓库根路径下需要打包进制品的相对目录；空表示整个仓库。
	// 例如 impeccable 只需要 .opencode/skills 等技能目录。
	includePaths []string
}

var builtinSkillSeeds = []builtinSkillSeed{
	{
		slug:         "hallmark",
		name:         "Hallmark",
		description:  "检查网页中的模板化设计问题，并提供主题、排版和视觉自检规则。",
		sourceURL:    "https://github.com/Nutlope/hallmark",
		license:      "MIT",
		includePaths: []string{"skills/hallmark"},
	},
	{
		slug:         "impeccable",
		name:         "Impeccable",
		description:  "提供界面设计命令、检查规则和浏览器迭代流程，用于设计与改进网页。",
		sourceURL:    "https://github.com/pbakaus/impeccable",
		license:      "Apache-2.0",
		includePaths: []string{".agents/skills/impeccable"},
	},
	{
		slug:         "taste-skill",
		name:         "Taste Skill",
		description:  "提供布局、排版、间距、动效和图像生成方面的前端设计指导。",
		sourceURL:    "https://github.com/Leonxlnx/taste-skill",
		license:      "MIT",
		includePaths: []string{"skills"},
	},
	{
		slug:         "gsap-skills",
		name:         "GSAP Skills",
		description:  "GSAP 官方使用指南，涵盖时间线、ScrollTrigger、插件、常见框架和性能优化。",
		sourceURL:    "https://github.com/greensock/gsap-skills",
		license:      "MIT",
		includePaths: []string{"skills"},
	},
}

// EnsureBuiltinSkillPackages 在服务启动时把内置商店条目 upsert 进 skill_packages。
// 幂等：按 slug 冲突更新描述；不会删除用户自定义条目。
func EnsureBuiltinSkillPackages(ctx context.Context, repository interface {
	UpsertSkillPackage(context.Context, domain.SkillPackage) (domain.SkillPackage, error)
}) error {
	for _, seed := range builtinSkillSeeds {
		item := domain.SkillPackage{
			Slug:        seed.slug,
			Name:        seed.name,
			Description: seed.description,
			SourceURL:   seed.sourceURL,
			License:     seed.license,
			Builtin:     true,
		}
		item.ID = "skp_builtin_" + seed.slug
		result, err := repository.UpsertSkillPackage(ctx, item)
		if err != nil {
			return fmt.Errorf("seed skill package %s: %w", seed.slug, err)
		}
		log.Printf("skill package seeded: %s (%s)", result.Slug, result.ID)
	}
	return nil
}

// BuiltinSkillIncludePaths 返回内置仓库打包时包含的路径（供发布流程生成制品）。
func BuiltinSkillIncludePaths(slug string) []string {
	for _, seed := range builtinSkillSeeds {
		if seed.slug == slug {
			return seed.includePaths
		}
	}
	return nil
}
