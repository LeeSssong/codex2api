#!/usr/bin/env python3
"""Reject changes from shared main; install without replacing existing hooks."""
import argparse
import pathlib
import subprocess


def check(root, branch, event, pushed_refs=()):
    root = pathlib.Path(root).resolve()
    if root == pathlib.Path('/Users/gongtengxinwen/Documents/codex2api').resolve():
        raise ValueError('共享保留目录禁止提交或推送；请使用独立任务 worktree，再由发布整合者合入 main')
    if event == 'pre-commit' and branch in ('main', 'master'):
        raise ValueError('main 只接收已验证任务提交的整合；请在独立任务分支提交')
    if event == 'pre-push':
        for local_ref, remote_ref in pushed_refs:
            if remote_ref == 'refs/heads/main' and local_ref != 'refs/heads/main':
                raise ValueError('禁止功能分支直接推送 main；使用干净发布 checkout 的 main')


def main():
    p = argparse.ArgumentParser()
    p.add_argument('event', choices=['pre-commit', 'pre-push'])
    a = p.parse_args()
    root = subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], text=True).strip()
    branch = subprocess.check_output(['git', 'branch', '--show-current'], text=True).strip()
    refs = []
    if a.event == 'pre-push':
        import sys
        for line in sys.stdin:
            fields = line.split()
            if len(fields) == 4:
                refs.append((fields[0], fields[2]))
    try:
        check(root, branch, a.event, refs)
    except ValueError as exc:
        raise SystemExit(str(exc))


if __name__ == '__main__':
    main()
