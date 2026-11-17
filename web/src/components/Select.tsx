import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useI18n } from "../i18n";

export type SelectOption = {
  value: string;
  label: string;
  hint?: string;
  keywords?: string;
  disabled?: boolean;
};

type Props = {
  value: string;
  options: SelectOption[];
  onChange: (value: string) => void;
  placeholder?: string;
  /** Show a search field inside the popover. */
  searchable?: boolean;
  searchPlaceholder?: string;
  emptyText?: string;
  disabled?: boolean;
  id?: string;
  className?: string;
};

type Placement = {
  top?: number;
  bottom?: number;
  left: number;
  width: number;
  maxHeight: number;
};

const POPOVER_GAP = 4;
const VIEWPORT_MARGIN = 8;
const POPOVER_MAX_HEIGHT = 300;

export default function Select({
  value,
  options,
  onChange,
  placeholder,
  searchable = false,
  searchPlaceholder,
  emptyText,
  disabled = false,
  id,
  className,
}: Props) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const [placement, setPlacement] = useState<Placement | null>(null);
  const wrapperRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const listId = useId();

  const selected = useMemo(
    () => options.find((option) => option.value === value),
    [options, value],
  );

  const filtered = useMemo(() => {
    if (!searchable) return options;
    const needle = query.trim().toLowerCase();
    if (!needle) return options;
    return options.filter((option) => (
      [option.label, option.value, option.hint, option.keywords]
        .some((text) => text?.toLowerCase().includes(needle))
    ));
  }, [options, query, searchable]);

  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current;
    if (!trigger) return;
    const rect = trigger.getBoundingClientRect();
    const below = window.innerHeight - rect.bottom - POPOVER_GAP - VIEWPORT_MARGIN;
    const above = rect.top - POPOVER_GAP - VIEWPORT_MARGIN;
    const openUp = below < 220 && above > below;
    const room = Math.max(96, openUp ? above : below);
    const minWidth = searchable ? 220 : rect.width;
    const width = Math.min(Math.max(rect.width, minWidth), window.innerWidth - VIEWPORT_MARGIN * 2);
    const left = Math.min(
      Math.max(rect.left, VIEWPORT_MARGIN),
      window.innerWidth - width - VIEWPORT_MARGIN,
    );
    setPlacement(openUp
      ? {
        bottom: window.innerHeight - rect.top + POPOVER_GAP,
        left,
        width,
        maxHeight: Math.min(room, POPOVER_MAX_HEIGHT),
      }
      : {
        top: rect.bottom + POPOVER_GAP,
        left,
        width,
        maxHeight: Math.min(room, POPOVER_MAX_HEIGHT),
      });
  }, [searchable]);

  useEffect(() => {
    if (!open) return;
    updatePosition();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  }, [open, updatePosition]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(event: PointerEvent) {
      const target = event.target as Node | null;
      if (!target) return;
      if (wrapperRef.current?.contains(target) || popoverRef.current?.contains(target)) return;
      setOpen(false);
      setQuery("");
    }
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => document.removeEventListener("pointerdown", onPointerDown, true);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const node = listRef.current?.querySelector<HTMLElement>(`[data-index="${activeIndex}"]`);
    node?.scrollIntoView({ block: "nearest" });
  }, [open, activeIndex]);

  useEffect(() => {
    if (!open) return;
    setActiveIndex((current) => Math.min(current, Math.max(filtered.length - 1, 0)));
  }, [open, filtered.length]);

  function openSelect(initialQuery = "") {
    if (disabled) return;
    const selectedIndex = options.findIndex((option) => option.value === value);
    setQuery(initialQuery);
    setActiveIndex(initialQuery ? 0 : Math.max(selectedIndex, 0));
    setOpen(true);
  }

  function closeSelect() {
    setOpen(false);
    setQuery("");
  }

  function choose(option: SelectOption | undefined) {
    if (!option || option.disabled) return;
    onChange(option.value);
    closeSelect();
    triggerRef.current?.focus();
  }

  function moveActive(delta: number) {
    if (!filtered.length) return;
    let next = activeIndex;
    for (let step = 0; step < filtered.length; step += 1) {
      next = (next + delta + filtered.length) % filtered.length;
      if (!filtered[next]?.disabled) break;
    }
    setActiveIndex(next);
  }

  function jumpToLetter(key: string) {
    const needle = key.toLowerCase();
    const match = filtered.findIndex((option) => (
      !option.disabled && option.label.toLowerCase().startsWith(needle)
    ));
    if (match < 0) return false;
    setActiveIndex(match);
    return true;
  }

  function onTriggerKeyDown(event: React.KeyboardEvent<HTMLButtonElement>) {
    if (open && !searchable) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        moveActive(1);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        moveActive(-1);
        return;
      }
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        choose(filtered[activeIndex]);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        closeSelect();
        return;
      }
      if (event.key === "Tab") {
        closeSelect();
        return;
      }
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      openSelect();
      return;
    }
    if (event.key.length === 1 && !event.metaKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
      if (searchable) {
        openSelect(event.key);
        return;
      }
      if (jumpToLetter(event.key)) setOpen(true);
    }
  }

  function onSearchKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      moveActive(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveActive(-1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      choose(filtered[activeIndex]);
    } else if (event.key === "Escape") {
      event.preventDefault();
      closeSelect();
      triggerRef.current?.focus();
    } else if (event.key === "Tab") {
      setOpen(false);
      setQuery("");
    }
  }

  const display = selected?.label ?? value;

  return (
    <div ref={wrapperRef} className={`select${className ? ` ${className}` : ""}`}>
      <button
        ref={triggerRef}
        type="button"
        id={id}
        className="select-trigger"
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-controls={open ? listId : undefined}
        aria-activedescendant={
          open && !searchable && filtered[activeIndex] ? `${listId}-${activeIndex}` : undefined
        }
        disabled={disabled}
        onClick={() => (open ? closeSelect() : openSelect())}
        onKeyDown={onTriggerKeyDown}
      >
        <span className={`select-value${display ? "" : " placeholder"}`}>
          {display || placeholder || t("select.none")}
        </span>
        <span className="select-caret" aria-hidden="true">▾</span>
      </button>
      {open && placement ? createPortal(
        <div
          ref={popoverRef}
          className="select-popover"
          style={{
            top: placement.top,
            bottom: placement.bottom,
            left: placement.left,
            width: placement.width,
            maxHeight: placement.maxHeight,
          }}
        >
          {searchable ? (
            <div className="select-search">
              <input
                autoFocus
                value={query}
                placeholder={searchPlaceholder ?? t("select.searchPlaceholder")}
                aria-label={searchPlaceholder ?? t("select.searchPlaceholder")}
                onChange={(event) => {
                  setQuery(event.target.value);
                  setActiveIndex(0);
                }}
                onKeyDown={onSearchKeyDown}
                role="combobox"
                aria-autocomplete="list"
                aria-expanded={open}
                aria-controls={listId}
                aria-activedescendant={filtered[activeIndex] ? `${listId}-${activeIndex}` : undefined}
              />
            </div>
          ) : null}
          <ul ref={listRef} className="select-list" id={listId} role="listbox">
            {filtered.map((option, index) => (
              <li
                key={option.value}
                id={`${listId}-${index}`}
                data-index={index}
                role="option"
                aria-selected={option.value === value}
                aria-disabled={option.disabled || undefined}
                className={`select-option${index === activeIndex ? " active" : ""}`}
                onMouseEnter={() => setActiveIndex(index)}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => choose(option)}
              >
                <span className="select-option-label">{option.label}</span>
                {option.hint ? <span className="select-option-hint">{option.hint}</span> : null}
              </li>
            ))}
          </ul>
          {!filtered.length ? (
            <div className="select-empty">{emptyText ?? t("select.empty")}</div>
          ) : null}
        </div>,
        document.body,
      ) : null}
    </div>
  );
}
