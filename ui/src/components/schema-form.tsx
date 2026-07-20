import { useMemo } from "react";
import { useForm, Controller, type Control, type FieldErrors, type UseFormRegister } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { zodForObject, defaultsFor } from "@/lib/schema-zod";
import { humanizeKey, type JSONSchema } from "@/lib/schema";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// The writable half of schema-driven rendering (ADR-0090 §5 / ADR-0003 L8): render + VALIDATE any
// Contract's input schema as a form, with zero per-Contract React. A plugin ships a schema and gets
// a working, validated form for free; no community code executes in the interface plane (§7.3).
// This slice covers the core widget set (string / long-text / number / boolean / enum / string
// array); richer x-* widgets (entity-ref, secret picker) arrive in a later slice.
type Values = Record<string, unknown>;

export function SchemaForm({
  schema,
  onSubmit,
  submitLabel = "Submit",
  submitting = false,
}: {
  schema: JSONSchema;
  onSubmit: (values: Values) => void;
  submitLabel?: string;
  submitting?: boolean;
}) {
  const resolver = useMemo(() => zodResolver(zodForObject(schema)), [schema]);
  const defaults = useMemo(() => defaultsFor(schema), [schema]);
  const {
    register,
    handleSubmit,
    control,
    formState: { errors },
  } = useForm<Values>({ resolver, defaultValues: defaults });

  const props = Object.entries(schema.properties ?? {});
  const required = new Set(schema.required ?? []);

  if (props.length === 0) {
    return (
      <form onSubmit={handleSubmit(onSubmit)}>
        <p className="mb-3 text-sm text-muted-foreground">This operation takes no parameters.</p>
        <Button type="submit" disabled={submitting}>
          {submitLabel}
        </Button>
      </form>
    );
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="grid gap-4">
      {props.map(([name, ps]) => (
        <Field
          key={name}
          name={name}
          schema={ps}
          required={required.has(name)}
          register={register}
          control={control}
          errors={errors}
        />
      ))}
      <div>
        <Button type="submit" disabled={submitting}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

function Field({
  name,
  schema,
  required,
  register,
  control,
  errors,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  register: UseFormRegister<Values>;
  control: Control<Values>;
  errors: FieldErrors<Values>;
}) {
  const label = schema.title ?? humanizeKey(name);
  const type = Array.isArray(schema.type) ? schema.type[0] : schema.type;
  const err = errors[name]?.message as string | undefined;
  const longText = schema.format === "long-text" || (schema.maxLength ?? 0) > 200;

  let control_: React.ReactNode;
  if (schema.enum && schema.enum.length) {
    control_ = (
      <select
        {...register(name)}
        className="h-8 w-full rounded-md border border-input bg-transparent px-2.5 text-[13px]"
      >
        <option value="">— select —</option>
        {schema.enum.map((o) => (
          <option key={String(o)} value={String(o)}>
            {String(o)}
          </option>
        ))}
      </select>
    );
  } else if (type === "boolean") {
    control_ = (
      <Controller
        control={control}
        name={name}
        render={({ field }) => (
          <input
            type="checkbox"
            checked={Boolean(field.value)}
            onChange={(e) => field.onChange(e.target.checked)}
            className="size-4 accent-[var(--primary)]"
          />
        )}
      />
    );
  } else if (type === "array") {
    // string arrays as comma-split chips (the common Actuator param shape); object arrays fall back.
    control_ = (
      <Controller
        control={control}
        name={name}
        render={({ field }) => (
          <Input
            placeholder="comma-separated"
            value={Array.isArray(field.value) ? field.value.join(", ") : ""}
            onChange={(e) =>
              field.onChange(
                e.target.value
                  .split(",")
                  .map((s) => s.trim())
                  .filter(Boolean),
              )
            }
          />
        )}
      />
    );
  } else if (longText) {
    control_ = (
      <textarea
        {...register(name)}
        rows={4}
        className="w-full rounded-md border border-input bg-transparent px-2.5 py-1.5 font-mono text-[13px]"
      />
    );
  } else {
    control_ = (
      <Input
        type={type === "number" || type === "integer" ? "number" : "text"}
        {...register(name)}
      />
    );
  }

  return (
    <label className="grid gap-1">
      <span className="text-sm font-medium">
        {label}
        {required && <span className="ml-0.5 text-destructive">*</span>}
      </span>
      {schema.description && <span className="text-xs text-muted-foreground">{schema.description}</span>}
      {control_}
      {err && <span className={cn("text-xs text-destructive")}>{err}</span>}
    </label>
  );
}
