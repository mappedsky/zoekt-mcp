package mcpserver

import (
	"context"
	"fmt"

	"github.com/mappedsky/zoekt-mcp/internal/gitrepo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GitLogInput walks history.
type GitLogInput struct {
	Repo  string `json:"repo" jsonschema:"Exact repository name, as reported by zoekt_list_repos."`
	Ref   string `json:"ref,omitempty" jsonschema:"Branch, tag, or commit to start from. Defaults to the clone's default branch."`
	Path  string `json:"path,omitempty" jsonschema:"Only return commits that touched this repository-relative path."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum commits to return. Defaults to the server limit."`
}

// GitShowInput reads one commit.
type GitShowInput struct {
	Repo         string `json:"repo" jsonschema:"Exact repository name."`
	Rev          string `json:"rev" jsonschema:"Commit SHA, branch, or tag to show."`
	IncludePatch bool   `json:"include_patch,omitempty" jsonschema:"Include the unified diff. Off by default because a patch is large; the per-file counts come back either way."`
}

// GitDiffInput compares two revisions.
type GitDiffInput struct {
	Repo         string `json:"repo" jsonschema:"Exact repository name."`
	From         string `json:"from" jsonschema:"Revision to compare from, for example a tag or commit."`
	To           string `json:"to" jsonschema:"Revision to compare to."`
	IncludePatch bool   `json:"include_patch,omitempty" jsonschema:"Include the unified diff for the whole comparison. Off by default."`
}

// GitBlameInput attributes lines of a file.
type GitBlameInput struct {
	Repo      string `json:"repo" jsonschema:"Exact repository name."`
	Path      string `json:"path" jsonschema:"Repository-relative file path."`
	Rev       string `json:"rev,omitempty" jsonschema:"Revision to blame at. Defaults to the clone's default branch."`
	StartLine int    `json:"start_line,omitempty" jsonschema:"First line to attribute, 1-based. Defaults to the start of the file."`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"Last line to attribute. Defaults to the end of the file, subject to the server limit."`
}

// GitFileInput reads a file at a revision.
type GitFileInput struct {
	Repo string `json:"repo" jsonschema:"Exact repository name."`
	Path string `json:"path" jsonschema:"Repository-relative file path."`
	Rev  string `json:"rev,omitempty" jsonschema:"Revision to read at. Defaults to the clone's default branch."`
}

// GitRefsInput lists refs.
type GitRefsInput struct {
	Repo string `json:"repo" jsonschema:"Exact repository name."`
}

// GitLogOutput is a page of history.
type GitLogOutput struct {
	Repo      string           `json:"repo" jsonschema:"Repository the history came from."`
	Commits   []gitrepo.Commit `json:"commits" jsonschema:"Commits, newest first."`
	Count     int              `json:"count" jsonschema:"Number of commits returned."`
	Truncated bool             `json:"truncated" jsonschema:"Whether more commits exist beyond the limit."`
}

// GitShowOutput is one commit and its churn.
type GitShowOutput struct {
	Repo           string             `json:"repo" jsonschema:"Repository the commit came from."`
	Commit         gitrepo.Commit     `json:"commit" jsonschema:"The commit."`
	Files          []gitrepo.FileStat `json:"files" jsonschema:"Per-file additions and deletions."`
	Patch          string             `json:"patch,omitempty" jsonschema:"Unified diff, when include_patch was set."`
	PatchTruncated bool               `json:"patch_truncated,omitempty" jsonschema:"Whether the patch was cut off at the server's byte limit."`
}

// GitDiffOutput compares two revisions.
type GitDiffOutput struct {
	Repo           string             `json:"repo" jsonschema:"Repository the comparison came from."`
	From           string             `json:"from" jsonschema:"Revision compared from."`
	To             string             `json:"to" jsonschema:"Revision compared to."`
	Files          []gitrepo.FileStat `json:"files" jsonschema:"Per-file additions and deletions."`
	Patch          string             `json:"patch,omitempty" jsonschema:"Unified diff, when include_patch was set."`
	PatchTruncated bool               `json:"patch_truncated,omitempty" jsonschema:"Whether the patch was cut off at the server's byte limit."`
}

// GitBlameOutput attributes a range of lines.
type GitBlameOutput struct {
	Repo      string              `json:"repo" jsonschema:"Repository the file came from."`
	Path      string              `json:"path" jsonschema:"File that was attributed."`
	Rev       string              `json:"rev,omitempty" jsonschema:"Revision the file was read at."`
	Lines     []gitrepo.BlameLine `json:"lines" jsonschema:"Attributed lines."`
	TotalLine int                 `json:"total_lines" jsonschema:"Total lines in the file at this revision."`
	Truncated bool                `json:"truncated" jsonschema:"Whether the range was cut short by the server limit."`
}

// GitFileOutput is a file at a revision.
type GitFileOutput struct {
	Repo      string `json:"repo" jsonschema:"Repository the file came from."`
	Path      string `json:"path" jsonschema:"File that was read."`
	Rev       string `json:"rev,omitempty" jsonschema:"Revision the file was read at."`
	Content   string `json:"content" jsonschema:"File content at that revision."`
	Bytes     int    `json:"bytes" jsonschema:"Bytes returned."`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"Whether the file was cut off at the server's byte limit."`
}

// GitRefsOutput lists branches and tags.
type GitRefsOutput struct {
	Repo  string        `json:"repo" jsonschema:"Repository the refs came from."`
	Refs  []gitrepo.Ref `json:"refs" jsonschema:"Branches and tags in the clone."`
	Count int           `json:"count" jsonschema:"Number of refs returned."`
}

// addGitTools registers history tools backed by the clones the index was built
// from. It is a no-op when no repository root is configured, so the search half
// can be deployed on its own without the volume.
func addGitTools(server *mcp.Server, store *gitrepo.Store, locator *repoLocator, config Config) {
	if store == nil {
		return
	}

	mcp.AddTool(server, tool(
		"git_log",
		"List commits, newest first, optionally only those touching one path. This is how to answer when something changed or whether a fix has already landed; the code search tools only see the indexed revision.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in GitLogInput) (*mcp.CallToolResult, GitLogOutput, error) {
		path, err := locate(ctx, locator, in.Repo)
		if err != nil {
			return nil, GitLogOutput{}, err
		}
		limit := clamp(in.Limit, config.MaxCommits)
		// Ask for one more than the limit so the caller learns there is more.
		commits, err := store.Log(path, gitrepo.LogOptions{Rev: in.Ref, Path: in.Path, Limit: limit + 1})
		if err != nil {
			return nil, GitLogOutput{}, err
		}
		out := GitLogOutput{Repo: in.Repo}
		if len(commits) > limit {
			out.Truncated = true
			commits = commits[:limit]
		}
		out.Commits = commits
		out.Count = len(commits)
		return nil, out, nil
	})

	mcp.AddTool(server, tool(
		"git_show",
		"Show one commit: author, date, message, and which files it changed with per-file line counts. Pass include_patch to add the diff itself.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in GitShowInput) (*mcp.CallToolResult, GitShowOutput, error) {
		path, err := locate(ctx, locator, in.Repo)
		if err != nil {
			return nil, GitShowOutput{}, err
		}
		if err := require("rev", in.Rev); err != nil {
			return nil, GitShowOutput{}, err
		}
		commit, files, patch, truncated, err := store.Show(path, in.Rev, in.IncludePatch, config.MaxPatchBytes)
		if err != nil {
			return nil, GitShowOutput{}, err
		}
		return nil, GitShowOutput{
			Repo: in.Repo, Commit: commit, Files: files,
			Patch: patch, PatchTruncated: truncated,
		}, nil
	})

	mcp.AddTool(server, tool(
		"git_diff",
		"Compare two revisions and report which files changed with per-file line counts. Use it to see what moved between two tags or commits, for example whether a dependency manifest changed between releases.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in GitDiffInput) (*mcp.CallToolResult, GitDiffOutput, error) {
		path, err := locate(ctx, locator, in.Repo)
		if err != nil {
			return nil, GitDiffOutput{}, err
		}
		if err := require("from", in.From); err != nil {
			return nil, GitDiffOutput{}, err
		}
		if err := require("to", in.To); err != nil {
			return nil, GitDiffOutput{}, err
		}
		files, patch, truncated, err := store.Diff(path, in.From, in.To, in.IncludePatch, config.MaxPatchBytes)
		if err != nil {
			return nil, GitDiffOutput{}, err
		}
		return nil, GitDiffOutput{
			Repo: in.Repo, From: in.From, To: in.To, Files: files,
			Patch: patch, PatchTruncated: truncated,
		}, nil
	})

	mcp.AddTool(server, tool(
		"git_blame",
		"Attribute each line of a file to the commit that last changed it. Narrow with start_line and end_line; blaming a whole large file is rarely what you want.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in GitBlameInput) (*mcp.CallToolResult, GitBlameOutput, error) {
		path, err := locate(ctx, locator, in.Repo)
		if err != nil {
			return nil, GitBlameOutput{}, err
		}
		if err := require("path", in.Path); err != nil {
			return nil, GitBlameOutput{}, err
		}
		start, end := in.StartLine, in.EndLine
		if start > 0 && end > 0 && end < start {
			return nil, GitBlameOutput{}, fmt.Errorf("end_line %d is before start_line %d", end, start)
		}
		if start <= 0 {
			start = 1
		}
		if end <= 0 || end-start+1 > config.MaxBlameLines {
			end = start + config.MaxBlameLines - 1
		}
		lines, total, err := store.Blame(path, in.Rev, in.Path, start, end)
		if err != nil {
			return nil, GitBlameOutput{}, err
		}
		return nil, GitBlameOutput{
			Repo: in.Repo, Path: in.Path, Rev: in.Rev,
			Lines: lines, TotalLine: total, Truncated: end < total,
		}, nil
	})

	mcp.AddTool(server, tool(
		"git_file",
		"Read a file as it was at any revision, including tags and commits the index does not cover. zoekt_get_file only reads indexed branches, so this is how to see a manifest at a specific release.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in GitFileInput) (*mcp.CallToolResult, GitFileOutput, error) {
		path, err := locate(ctx, locator, in.Repo)
		if err != nil {
			return nil, GitFileOutput{}, err
		}
		if err := require("path", in.Path); err != nil {
			return nil, GitFileOutput{}, err
		}
		content, truncated, err := store.File(path, in.Rev, in.Path, config.MaxFileBytes)
		if err != nil {
			return nil, GitFileOutput{}, err
		}
		return nil, GitFileOutput{
			Repo: in.Repo, Path: in.Path, Rev: in.Rev,
			Content: content, Bytes: len(content), Truncated: truncated,
		}, nil
	})

	mcp.AddTool(server, tool(
		"git_refs",
		"List the branches and tags the clone holds, with the date of each tip. The clone carries every fetched branch, which is more than the index covers.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in GitRefsInput) (*mcp.CallToolResult, GitRefsOutput, error) {
		path, err := locate(ctx, locator, in.Repo)
		if err != nil {
			return nil, GitRefsOutput{}, err
		}
		refs, err := store.Refs(path)
		if err != nil {
			return nil, GitRefsOutput{}, err
		}
		return nil, GitRefsOutput{Repo: in.Repo, Refs: refs, Count: len(refs)}, nil
	})
}

func locate(ctx context.Context, locator *repoLocator, repo string) (string, error) {
	if err := require("repo", repo); err != nil {
		return "", err
	}
	return locator.Path(ctx, repo)
}
