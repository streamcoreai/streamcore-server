import { StreamCoreAIPlugin } from '@streamcore/plugin';

const plugin = new StreamCoreAIPlugin();

plugin.onExecute((params: Record<string, unknown>) => {
  const expression = params.expression as string;
  if (!expression) {
    throw new Error("Missing required parameter: expression");
  }

  // Only allow safe math operations — no arbitrary code execution.
  const sanitized = expression.replace(/[^0-9+\-*/().,%^ Math.sqrtpowsincostalogPIE\s]/g, "");
  if (sanitized !== expression) {
    throw new Error("Expression contains disallowed characters");
  }

  // Use Function constructor with Math in scope for safe evaluation.
  const mathFn = new Function(
    "Math",
    `"use strict"; return (${sanitized});`
  );
  const result = mathFn(Math);

  if (typeof result !== "number" || !isFinite(result)) {
    throw new Error(`Invalid result: ${result}`);
  }

  return `The result is ${result}`;
});

plugin.run();
