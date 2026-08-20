export interface ProblemDetails {
  type: string;
  title: string;
  status: number;
  detail?: string;
  instance?: string;
  code: string;
  requestId?: string;
}

export class AepProblem extends Error {
  readonly type: string;
  readonly status: number;
  readonly code: string;
  readonly detail?: string;
  readonly requestId?: string;

  constructor(problem: ProblemDetails) {
    super(problem.detail ?? problem.title);
    this.name = 'AepProblem';
    this.type = problem.type;
    this.status = problem.status;
    this.code = problem.code;
    this.detail = problem.detail;
    this.requestId = problem.requestId;
  }

  static from(status: number, value: unknown, requestId?: string | null): AepProblem {
    const candidate = value as Partial<ProblemDetails> | null;
    return new AepProblem({
      type: candidate?.type ?? 'about:blank',
      title: candidate?.title ?? `AEP request failed with status ${status}`,
      status,
      detail: candidate?.detail,
      code: candidate?.code ?? 'HTTP_ERROR',
      requestId: candidate?.requestId ?? requestId ?? undefined,
    });
  }
}
