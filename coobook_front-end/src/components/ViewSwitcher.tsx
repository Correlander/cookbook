export interface ViewSwitcherOption<Value extends string> {
  value: Value;
  label: string;
}

export default function ViewSwitcher<Value extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: Value;
  options: readonly ViewSwitcherOption<Value>[];
  onChange: (value: Value) => void;
}) {
  return (
    <div className="view-switcher" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          className={value === option.value ? "view-switcher__option is-selected" : "view-switcher__option"}
          type="button"
          key={option.value}
          aria-pressed={value === option.value}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
