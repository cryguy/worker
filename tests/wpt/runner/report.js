// Custom testharnessreport.js for worker-combo WPT runner.
// Collects test results into globalThis.__wpt_results instead of
// writing to DOM or stderr. The Go runner reads this after execution.

globalThis.__wpt_results = [];
globalThis.__wpt_harness_status = null;
globalThis.__wpt_done = false;

add_result_callback(function(test) {
  globalThis.__wpt_results.push({
    name: test.name,
    status: test.status,
    message: test.message || null,
    stack: test.stack || null
  });
});

add_completion_callback(function(tests, harnessStatus) {
  globalThis.__wpt_harness_status = {
    status: harnessStatus.status,
    message: harnessStatus.message || null
  };
  globalThis.__wpt_done = true;
});
