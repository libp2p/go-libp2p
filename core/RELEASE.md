# Release Guidelines

## Recommended Reading

- https://go.dev/doc/modules/managing-source

## Process

1. Collect the relevant commits:

```
git log --pretty=format:"%h %s %an <%ae>" -- core/
```

or

```
jj log --no-graph -T 'commit_id.short() ++ " " ++ description.first_line() ++ " " ++ author ++ "\n"' core
```

2. If it's a patch compatible change, only update the patch number. Otherwise,
   match the version number of the last go-libp2p release.

3. `git tag core/$VERSION` With the following loose template (adjust as needed)

```
vX.Y.Z – [Release type: Major | Minor | Patch]

Summary:
- Short sentence describing the purpose of the release (e.g. new feature, bugfixes, breaking changes).

Changes:
- [Added/Changed/Fixed] <package/type/function>: <short description>
- [Added/Changed/Fixed] <package/type/function>: <short description>
- …

Breaking Changes (if any):
- <Describe removed/renamed types, functions, or changed behavior>
- <Mention minimum supported Go version if bumped>

Notes:
- <Optional: migration instructions, links to changelog, docs, or release notes>
```

4. Publish the tag `git push origin core/$VERSION`
