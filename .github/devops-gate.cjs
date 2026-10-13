'use strict';

// Trusted-default-branch CI aggregation. Only writes the gate check;
// never checks out PR code, downloads artifacts, publishes packages or deploys.
function validatePr(pr) {
  const errors = [];
  // Dependabot's generated release notes do not use our manual PR template.
  if (pr.user?.login === 'dependabot[bot]' && pr.user?.type === 'Bot') {
    if (!/^(build|chore)(\([a-z0-9._/-]+\))?: .+/.test(pr.title || '') || !(pr.body || '').trim()) {
      errors.push('Dependabot PRs still require a conventional dependency title and nonempty release notes.');
    }
    return errors;
  }
  if (
    !/^(feat|fix|docs|refactor|test|perf|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: [a-z][^\n]*$/.test(
      pr.title || '',
    )
  ) {
    errors.push('Title must use type(scope): summary with an English lowercase summary.');
  }
  let body = (pr.body || '').replace(/\r/g, '');
  while (body.includes('<!--')) {
    const start = body.indexOf('<!--');
    const end = body.indexOf('-->', start + 4);
    body = body.slice(0, start) + (end === -1 ? '' : body.slice(end + 3));
  }
  const sections = [...body.matchAll(/^##\s+([^\n]+)\n([\s\S]*?)(?=^#{1,2}\s|$(?![\s\S]))/gm)];
  for (const names of [
    ['改动', 'What changed', 'Summary', 'Changes'],
    ['原因', 'Why', 'Reason'],
    ['验证', 'Validation', 'Testing'],
  ]) {
    const section = sections.find((match) =>
      names.some((name) => match[1].trim().toLowerCase() === name.toLowerCase()),
    );
    const content = section?.[2].replace(/^[\s\-*]+$/gm, '').trim();
    if (!content || /^(?:todo|tbd|n\/a|待填写|待补充|\.\.\.)$/i.test(content))
      errors.push(`Fill the ${names[0]} section.`);
  }
  if (/^fix(?:\(|!?\s*:)/.test(pr.title || '')) {
    for (const [label, pattern] of [
      ['trigger', /触发|复现|reproduc|trigger/i],
      ['cause', /根因|原因|root cause|cause/i],
      ['before/after behavior', /修复前|修复后|before|after/i],
    ])
      if (!pattern.test(body.replace(/^#{1,6}\s+.*$/gm, ''))) errors.push(`Bug fixes must explain ${label}.`);
  }
  return errors;
}

function globMatches(path, pattern) {
  if (/[[\]+]/.test(pattern)) throw new Error(`Unsupported filter pattern: ${pattern}; update the gate matcher.`);
  let source = '^';
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i];
    if (c === '*' && pattern[i + 1] === '*') {
      i++;
      if (pattern[i + 1] === '/') {
        i++;
        source += '(?:.*/)?';
      } else source += '.*';
    } else if (c === '*') source += '[^/]*';
    else if (c === '?') source += '[^/]';
    else source += c.replace(/[\\^$+?.()|{}[\]]/g, '\\$&');
  }
  return new RegExp(`${source}$`).test(path);
}

function workflowApplies(workflow, files, branch) {
  const matches = (value, patterns) =>
    patterns.reduce((included, pattern) => {
      const negative = pattern.startsWith('!');
      return globMatches(value, negative ? pattern.slice(1) : pattern) ? !negative : included;
    }, false);
  if (workflow.branches && !matches(branch, workflow.branches)) return false;
  if (workflow.branchesIgnore && matches(branch, workflow.branchesIgnore)) return false;
  if (workflow.paths) return files.some((file) => matches(file, workflow.paths));
  if (workflow.pathsIgnore) return files.some((file) => !matches(file, workflow.pathsIgnore));
  return true;
}

function linkedRun(run, pr) {
  return run.head_repository?.id === pr.head.repo?.id && run.head_branch === pr.head.ref &&
    run.pull_requests?.some(link => link.number === pr.number && link.head?.sha === pr.head.sha &&
      link.base?.ref === pr.base.ref);
}

function evaluateRuns(workflows, runs, sha, pr) {
  const waiting = [],
    failed = [];
  for (const workflow of workflows) {
    const latest = runs
      .filter(
        (run) => run.head_sha === sha && run.event === 'pull_request' && run.path?.split('@')[0] === workflow.path &&
          (!pr || linkedRun(run, pr)),
      )
      .sort((a, b) => b.id - a.id || b.run_attempt - a.run_attempt)[0];
    if (latest?.status !== 'completed') waiting.push(workflow.name);
    else if (latest.conclusion !== 'success') failed.push(`${workflow.name}: ${latest.conclusion}`);
  }
  return { waiting, failed };
}

function samePr(left, right) {
  const snapshot = pr => JSON.stringify([
    pr.number, pr.state, pr.head.sha, pr.head.repo?.id, pr.head.ref,
    pr.base.ref, pr.title, pr.body, pr.changed_files,
  ]);
  return snapshot(left) === snapshot(right);
}

// Old heads retain independent checks during migration. Only an authoritative
// current-head PR run can select the unified entry; any legacy runs on that
// same head remain required, so parallel failures cannot be hidden.
function selectWorkflows(config, runs, pr, expected) {
  const belongs = (workflow, matchName = false) => runs.some((item) =>
    item.event === 'pull_request' && item.head_sha === pr.head.sha &&
    item.path?.split('@')[0] === workflow.path && (!matchName || item.name === workflow.name) && linkedRun(item, pr));
  if (!config.orchestrator || !belongs(config.orchestrator, true)) return expected;
  return [config.orchestrator, ...expected.filter((workflow) =>
    workflow.path !== config.orchestrator.path && belongs(workflow))];
}

async function run({ github, context, core, config }) {
  const pr = context.payload.pull_request;
  if (!pr) throw new Error('This gate only runs for pull requests.');
  const current = (await github.rest.pulls.get({ ...context.repo, pull_number: pr.number })).data;
  if (!samePr(current, pr) || current.state !== 'open')
    throw new Error('PR head changed, metadata changed or PR closed; use the newest gate event.');
  const errors = validatePr(pr);
  if (errors.length) throw new Error(errors.join('\n'));
  const files = await github.paginate(github.rest.pulls.listFiles, {
    ...context.repo,
    pull_number: pr.number,
    per_page: 100,
  });
  // GitHub returns at most 3000 changed files; fail closed rather than miss a filtered workflow.
  if (files.length !== pr.changed_files)
    throw new Error('Incomplete PR file list; split the PR or inspect the API limit.');
  const expected = (config.legacyWorkflows ?? config.workflows).filter((workflow) =>
    workflowApplies(
      workflow,
      files.flatMap((file) => [file.filename, ...(file.previous_filename ? [file.previous_filename] : [])]),
      pr.base.ref,
    ),
  );
  const runs = await github.paginate(github.rest.actions.listWorkflowRunsForRepo, {
    ...context.repo, event: 'pull_request', head_sha: pr.head.sha, per_page: 100,
  });
  const selected = selectWorkflows(config, runs, pr, expected);
  core.info(`Required existing workflows: ${selected.map((workflow) => workflow.name).join(', ') || 'none for this diff'}`);
  const { waiting, failed } = evaluateRuns(selected, runs, pr.head.sha, pr);
  if (failed.length) throw new Error(failed.join('\n'));
  if (waiting.length) {
    core.info(`Waiting for: ${waiting.join(', ')}; a workflow event will refresh this check.`);
    return { status: 'queued', summary: `Waiting for: ${waiting.join(', ')}` };
  }
  return { status: 'completed', conclusion: 'success', summary: 'PR metadata and all applicable CI workflows passed.' };
}

async function eventPullNumbers({ github, context, config }) {
  if (context.eventName === 'pull_request_target') return [context.payload.pull_request.number];
  if (context.eventName === 'workflow_dispatch') {
    if (context.ref !== 'refs/heads/' + context.payload.repository?.default_branch)
      throw new Error('Manual gate refresh must use the default branch.');
    const number = String(context.payload.inputs?.pull_request ?? '');
    if (!/^[1-9][0-9]*$/.test(number) || !Number.isSafeInteger(Number(number)))
      throw new Error('A valid pull request number is required.');
    return [Number(number)];
  }
  if (context.eventName !== 'workflow_run') throw new Error('Unsupported gate event.');
  // Fetch authoritative run metadata; the event's conclusion and PR hints are not authority.
  const run = (await github.rest.actions.getWorkflowRun({ ...context.repo, run_id: context.payload.workflow_run.id })).data;
  if (run.event !== 'pull_request' || ![...(config.legacyWorkflows ?? config.workflows), ...(config.orchestrator ? [config.orchestrator] : [])].some(workflow => workflow.path === run.path?.split('@')[0])) return [];
  const numbers = [...new Set((run.pull_requests ?? []).map(pr => pr.number))];
  const prs = numbers.length
    ? await Promise.all(numbers.map(async number => (await github.rest.pulls.get({ ...context.repo, pull_number: number })).data))
    : await github.paginate(github.rest.pulls.list, { ...context.repo, state: 'open', per_page: 100 });
  return prs.filter(pr => pr.state === 'open' && pr.head.sha === run.head_sha &&
    pr.head.repo?.id === run.head_repository?.id && pr.head.ref === run.head_branch &&
    (!numbers.length || linkedRun(run, pr))).map(pr => pr.number);
}

async function refresh({ github, context, core, config }) {
  const numbers = await eventPullNumbers({ github, context, config });
  for (const number of numbers) {
    const pr = (await github.rest.pulls.get({ ...context.repo, pull_number: number })).data;
    if (pr.state !== 'open') continue;
    const externalId = `devops-gate:${number}:${pr.head.sha}`;
    const checks = await github.paginate(github.rest.checks.listForRef, {
      ...context.repo, ref: pr.head.sha, check_name: 'ci-gate', filter: 'all', per_page: 100,
    });
    const owned = checks.filter(check => check.external_id === externalId && check.app?.slug === 'github-actions')
      .sort((a, b) => b.id - a.id)[0];
    const pending = {
      ...context.repo, status: 'queued',
      details_url: `${context.serverUrl}/${context.repo.owner}/${context.repo.repo}/actions/runs/${context.runId}`,
      output: { title: 'CI aggregation pending', summary: 'Checking current PR metadata and applicable workflows.' },
    };
    // Reuse only our own check. Reset pending before taking a new snapshot;
    // omit conclusion, since that field would force the API status to completed.
    const check = (owned
      ? await github.rest.checks.update({ ...pending, check_run_id: owned.id })
      : await github.rest.checks.create({ ...pending, name: 'ci-gate', head_sha: pr.head.sha, external_id: externalId })).data;
    let result;
    try {
      // Actions Context.repo is a prototype getter; spreading Context drops it.
      result = await run({ github, context: { repo: context.repo, payload: { pull_request: pr } }, core, config });
    } catch (error) {
      result = { status: 'completed', conclusion: 'failure', summary: String(error.message).slice(0, 20000) };
    }
    // Revalidate head and edited metadata immediately before publishing a result.
    const current = (await github.rest.pulls.get({ ...context.repo, pull_number: number })).data;
    if (!samePr(current, pr) || current.state !== 'open')
      result = { status: 'completed', conclusion: 'cancelled', summary: 'Superseded PR head or metadata; awaiting its gate event.' };
    await github.rest.checks.update({
      ...context.repo, check_run_id: check.id, status: result.status,
      ...(result.conclusion ? { conclusion: result.conclusion } : {}),
      output: { title: result.conclusion === 'success' ? 'CI aggregation passed' :
        result.conclusion ? 'CI aggregation blocked' : 'CI aggregation pending', summary: result.summary },
    });
    core.info(`PR #${number}: ${result.conclusion ?? result.status}`);
  }
}

module.exports = { validatePr, globMatches, workflowApplies, evaluateRuns, run, refresh, eventPullNumbers, samePr };
