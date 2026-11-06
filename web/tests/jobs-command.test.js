import { expect, it } from 'vitest';
import { parseJobsCommand } from '../ui/components/agent/jobs-command';

it('routes OMP job commands without sending a model prompt', () => {
  expect(parseJobsCommand('')).toEqual({ full: false, payload: { op: 'get_jobs' } });
  expect(parseJobsCommand(' full ')).toEqual({ full: true, payload: { op: 'get_jobs' } });
  expect(parseJobsCommand('kill job-1')).toEqual({ full: false, payload: { op: 'cancel_job', id: 'job-1' } });
  expect(parseJobsCommand('kill all')).toEqual({ full: false, payload: { op: 'cancel_job', id: 'all' } });
  for (const invalid of ['kill', 'full extra', 'kill one two', 'unknown']) expect(parseJobsCommand(invalid)).toBeNull();
});
