import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from 'react';

type DialogTone = 'default' | 'danger';

type ConfirmOptions = {
  title: string;
  description?: string;
  confirmLabel: string;
  cancelLabel: string;
  tone?: DialogTone;
};

type PromptOptions = ConfirmOptions & {
  inputLabel: string;
  defaultValue?: string;
  placeholder?: string;
  inputType?: 'text' | 'password';
  inputRequired?: boolean;
};

type ActiveDialog =
  | (ConfirmOptions & { kind: 'confirm'; resolve: (value: boolean) => void })
  | (PromptOptions & { kind: 'prompt'; resolve: (value: string | null) => void });

type MemberDialogContextValue = {
  confirm: (options: ConfirmOptions) => Promise<boolean>;
  prompt: (options: PromptOptions) => Promise<string | null>;
};

const MemberDialogContext = createContext<MemberDialogContextValue | null>(null);

export function useMemberDialog(): MemberDialogContextValue {
  const value = useContext(MemberDialogContext);
  if (!value) {
    throw new Error('useMemberDialog must be used within MemberDialogProvider');
  }
  return value;
}

export function MemberDialogProvider({ children }: { children: ReactNode }) {
  const [active, setActive] = useState<ActiveDialog | null>(null);
  const [inputValue, setInputValue] = useState('');
  const activeRef = useRef<ActiveDialog | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLElement | null>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const previousOverflowRef = useRef('');
  const dialogId = useId();

  const settle = useCallback((value: boolean | string | null) => {
    const current = activeRef.current;
    if (!current) return;
    activeRef.current = null;
    setActive(null);
    if (current.kind === 'confirm') current.resolve(Boolean(value));
    else current.resolve(typeof value === 'string' ? value : null);
  }, []);

  const cancel = useCallback(() => {
    settle(activeRef.current?.kind === 'confirm' ? false : null);
  }, [settle]);

  const replaceActive = useCallback((next: ActiveDialog) => {
    const current = activeRef.current;
    if (!current && document.activeElement instanceof HTMLElement) restoreFocusRef.current = document.activeElement;
    if (current) {
      if (current.kind === 'confirm') current.resolve(false);
      else current.resolve(null);
    }
    activeRef.current = next;
    setActive(next);
  }, []);

  const confirm = useCallback((options: ConfirmOptions) => new Promise<boolean>((resolve) => {
    replaceActive({ ...options, kind: 'confirm', resolve });
  }), [replaceActive]);

  const prompt = useCallback((options: PromptOptions) => new Promise<string | null>((resolve) => {
    setInputValue(options.defaultValue ?? '');
    replaceActive({ ...options, kind: 'prompt', resolve });
  }), [replaceActive]);

  useEffect(() => {
    if (!active) {
      if (restoreFocusRef.current && document.contains(restoreFocusRef.current)) {
        restoreFocusRef.current.focus();
      }
      restoreFocusRef.current = null;
      return undefined;
    }
    previousOverflowRef.current = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    if (active.kind === 'prompt') inputRef.current?.focus();
    return () => {
      document.body.style.overflow = previousOverflowRef.current;
    };
  }, [active]);

  const handleDialogKeyDown = useCallback((event: KeyboardEvent<HTMLElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      cancel();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href]');
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }, [cancel]);

  const value = { confirm, prompt };
  const titleId = `${dialogId}-title`;
  const descriptionId = `${dialogId}-description`;
  const inputId = `${dialogId}-input`;

  return (
    <MemberDialogContext.Provider value={value}>
      {children}
      {active ? (
        <div className="member-dialog-backdrop" role="presentation">
          {active.kind === 'prompt' ? (
            <form
              ref={(element) => { dialogRef.current = element; }}
              className="member-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby={titleId}
              aria-describedby={active.description ? descriptionId : undefined}
              tabIndex={-1}
              onKeyDown={handleDialogKeyDown}
              onSubmit={(event: FormEvent<HTMLFormElement>) => {
                event.preventDefault();
                settle(inputValue);
              }}
            >
              <h2 id={titleId}>{active.title}</h2>
              {active.description ? <p id={descriptionId} className="member-dialog-description">{active.description}</p> : null}
              <label className="member-dialog-field" htmlFor={inputId}>
                {active.inputLabel}
                <input
                  ref={inputRef}
                  id={inputId}
                  type={active.inputType ?? 'text'}
                  value={inputValue}
                  onChange={(event) => setInputValue(event.target.value)}
                  placeholder={active.placeholder}
                  required={active.inputRequired}
                  autoComplete="off"
                />
              </label>
              <div className="member-dialog-actions">
                <button type="button" onClick={cancel} autoFocus>{active.cancelLabel}</button>
                <button className={active.tone === 'danger' ? 'member-primary member-modal-danger' : 'member-primary'} type="submit">{active.confirmLabel}</button>
              </div>
            </form>
          ) : (
            <div
              ref={(element) => { dialogRef.current = element; }}
              className="member-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby={titleId}
              aria-describedby={active.description ? descriptionId : undefined}
              tabIndex={-1}
              onKeyDown={handleDialogKeyDown}
            >
              <h2 id={titleId}>{active.title}</h2>
              {active.description ? <p id={descriptionId} className="member-dialog-description">{active.description}</p> : null}
              <div className="member-dialog-actions">
                <button type="button" onClick={cancel} autoFocus>{active.cancelLabel}</button>
                <button className={active.tone === 'danger' ? 'member-primary member-modal-danger' : 'member-primary'} type="button" onClick={() => settle(true)}>{active.confirmLabel}</button>
              </div>
            </div>
          )}
        </div>
      ) : null}
    </MemberDialogContext.Provider>
  );
}
