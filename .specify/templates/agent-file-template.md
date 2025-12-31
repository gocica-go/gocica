# [PROJECT NAME] Development Guidelines

Auto-generated from all feature plans. Last updated: [DATE]

## Active Technologies

[EXTRACTED FROM ALL PLAN.MD FILES]

## Project Structure

```text
[ACTUAL STRUCTURE FROM PLANS]
```

## Commands

[ONLY COMMANDS FOR ACTIVE TECHNOLOGIES]

## Code Style

[LANGUAGE-SPECIFIC, ONLY FOR LANGUAGES IN USE]

## Recent Changes

[LAST 3 FEATURES AND WHAT THEY ADDED]

<!-- MANUAL ADDITIONS START -->
## Development Guidelines

You are working on a project that follows these strict development practices:

### Development Methodology
- Follow t-wada's TDD (Test-Driven Development) approach
- Make one commit per completed task

### Task Completion Requirements
After completing each task, you **MUST** perform ALL of the following steps in order:

1. **Code Quality Checks**
   - Run build process
   - Run lint checks
   - Run all tests

2. **Functional Verification**
   - Perform manual functionality testing to confirm the feature works as expected

3. **Pre-Documentation Update Steps**
   - Conduct code review using Codex MCP
   - Verify completion criteria with the project-planner sub-agent

4. **Documentation Update**
   - Update task progress in `docs/llm/PLAN.md`

### Important Notes
- Do NOT skip any of these steps
- Do NOT update `docs/llm/PLAN.md` until steps 1-3 are completed
- Always perform these steps in the specified order
<!-- MANUAL ADDITIONS END -->
