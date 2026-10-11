import { closestCenter, DndContext, type DragEndEvent, DragOverlay, PointerSensor, type UniqueIdentifier, useSensor, useSensors } from "@dnd-kit/core"
import { arrayMove, SortableContext, useSortable } from "@dnd-kit/sortable"
import { DotsSixVerticalIcon, EyeSlashIcon } from "@phosphor-icons/react"
import { t } from "i18next"
import { AnimatePresence, motion } from "motion/react"
import { type KeyboardEvent, useState } from "react"
import { IconTile } from "@/components/icon-tile"
import { rise } from "@/lib/motion"
import { Button } from "@/components/ui/button"
import type { Widget } from "@/features/preferences/api"
import { msg } from "@/lib/i18n"
import { cn } from "@/lib/utils"
import { WidgetOptions } from "./options-menu"
import { defaultOptions, widgetOf } from "./widgets"

/** One column on small screens, two on medium ones and three on large ones. */
const spans = { 1: "", 2: "md:col-span-2", 3: "md:col-span-2 lg:col-span-3" }
const widths = { 1: msg("One column"), 2: msg("Two columns"), 3: msg("Full width") }

const titleOf = (id: UniqueIdentifier) => t(widgetOf(String(id))?.title ?? "")

/**
 * The widgets of the overview. While editing, each can be dragged to the place of another,
 * moved a place with the arrow keys on its handle, resized and hidden; the others glide to
 * their new places.
 */
export function WidgetGrid({ layout, editing, onChange }: { layout: Widget[]; editing: boolean; onChange: (layout: Widget[]) => void }) {
  const [dragged, setDragged] = useState<string>()
  const [announcement, setAnnouncement] = useState("")
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }))
  const shown = layout.filter((w) => !w.hidden)
  const change = (id: string, to: Partial<Widget>) => onChange(layout.map((w) => (w.id === id ? { ...w, ...to } : w)))
  const swap = (id: UniqueIdentifier, other: UniqueIdentifier) =>
    onChange(arrayMove(layout, layout.findIndex((w) => w.id === id), layout.findIndex((w) => w.id === other)))

  function onDragEnd({ active, over }: DragEndEvent) {
    setDragged(undefined)
    if (over && active.id !== over.id) swap(active.id, over.id)
  }

  // Widgets of different sizes don't line up, so the keyboard moves one a place earlier or later.
  function onKey(event: KeyboardEvent, id: string) {
    const step = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }[event.key]
    if (!step) return
    event.preventDefault()
    const index = shown.findIndex((w) => w.id === id)
    const other = shown[index + step]
    if (!other) return
    swap(id, other.id)
    setAnnouncement(t("Moved {{title}} to place {{place}} of {{total}}.", { title: titleOf(id), place: index + step + 1, total: shown.length }))
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragStart={({ active }) => setDragged(String(active.id))}
      onDragEnd={onDragEnd}
      onDragCancel={() => setDragged(undefined)}
      accessibility={{
        screenReaderInstructions: {
          draggable: t("Drag the widget to the place of another, or move it a place earlier or later with the arrow keys."),
        },
        announcements: {
          onDragStart: ({ active }) => t("Picked up {{title}}.", { title: titleOf(active.id) }),
          onDragOver: ({ active, over }) =>
            over && over.id !== active.id
              ? t("{{title}} would take the place of {{other}}.", { title: titleOf(active.id), other: titleOf(over.id) })
              : t("{{title}} would stay where it is.", { title: titleOf(active.id) }),
          onDragEnd: ({ active, over }) =>
            over && over.id !== active.id
              ? t("Moved {{title}} to the place of {{other}}.", { title: titleOf(active.id), other: titleOf(over.id) })
              : t("{{title}} stays where it is.", { title: titleOf(active.id) }),
          onDragCancel: ({ active }) => t("{{title}} stays where it is.", { title: titleOf(active.id) }),
        },
      }}
    >
      {/* The widgets stay in place while one is dragged; the one it is dropped on shows its outline. */}
      <SortableContext items={shown.map((w) => w.id)} strategy={() => null}>
        <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-3">
          <AnimatePresence>
            {shown.map((w, i) => (
              <Frame
                key={w.id}
                widget={w}
                index={i}
                editing={editing}
                onChange={(to) => change(w.id, to)}
                onKeyDown={(event) => onKey(event, w.id)}
              />
            ))}
          </AnimatePresence>
        </div>
      </SortableContext>
      <DragOverlay>{dragged && <Ghost id={dragged} />}</DragOverlay>
      <p aria-live="polite" className="sr-only">
        {announcement}
      </p>
    </DndContext>
  )
}

function Frame({
  widget,
  index,
  editing,
  onChange,
  onKeyDown,
}: {
  widget: Widget
  index: number
  editing: boolean
  onChange: (to: Partial<Widget>) => void
  onKeyDown: (event: KeyboardEvent) => void
}) {
  const def = widgetOf(widget.id)
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, isDragging, isOver } = useSortable({ id: widget.id, disabled: !editing })
  if (!def) return null
  const title = t(def.title)
  const defaults = defaultOptions(def)
  const options = { ...defaults, ...widget.options }
  const setOption = (key: string, value: string | undefined) => {
    const next = { ...widget.options, [key]: value ?? "" }
    if (!value || value === defaults[key]) delete next[key] // back to the widget's default
    onChange({ options: next })
  }
  return (
    <motion.div
      ref={setNodeRef}
      layout
      {...rise(index)}
      exit={{ opacity: 0, scale: 0.96 }}
      transition={{ ...rise(index).transition, layout: { type: "spring", bounce: 0.15, duration: 0.5 } }}
      className={cn(
        "@container relative min-w-0 rounded-xl outline-offset-6",
        spans[widget.columns],
        // The links and choices in the header of a widget make room for its tools.
        editing && "outline-2 outline-border outline-dashed **:data-[slot=panel-actions]:invisible",
        isOver && !isDragging && "outline-primary",
      )}
    >
      {editing && (
        <div className="absolute -top-4 right-3 z-20 flex items-center gap-1 rounded-full bg-popover p-1 shadow-lg ring-1 ring-foreground/10">
          <Button
            ref={setActivatorNodeRef}
            variant="ghost"
            size="icon-xs"
            className="cursor-grab touch-none rounded-full active:cursor-grabbing"
            aria-label={t("Move {{title}}", { title })}
            title={t("Move {{title}}", { title })}
            {...attributes}
            {...listeners}
            onKeyDown={onKeyDown}
          >
            <DotsSixVerticalIcon weight="bold" />
          </Button>
          <div role="radiogroup" aria-label={t("Width of {{title}}", { title })} className="flex items-center max-md:hidden">
            {([1, 2, 3] as const).map((columns) => (
              <button
                key={columns}
                type="button"
                role="radio"
                aria-checked={widget.columns === columns}
                aria-label={t(widths[columns])}
                title={t(widths[columns])}
                onClick={() => onChange({ columns })}
                className="grid h-6 place-items-center rounded-full px-1.5 text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring aria-checked:bg-primary/10 aria-checked:text-primary"
              >
                <span aria-hidden className="flex gap-0.5">
                  {[1, 2, 3].map((i) => (
                    <span key={i} className={cn("h-3 w-1 rounded-[1px] bg-current", i > columns && "opacity-30")} />
                  ))}
                </span>
              </button>
            ))}
          </div>
          {def.options && <WidgetOptions title={title} defs={def.options} options={options} setOption={setOption} />}
          <Button
            variant="ghost"
            size="icon-xs"
            className="rounded-full"
            aria-label={t("Hide {{title}}", { title })}
            title={t("Hide {{title}}", { title })}
            onClick={() => onChange({ hidden: true })}
          >
            <EyeSlashIcon />
          </Button>
        </div>
      )}
      {/* While editing, the widget can't be used, so that dragging it doesn't open anything. */}
      <div inert={editing} className={cn("h-full transition-opacity", editing && "pointer-events-none select-none", isDragging && "opacity-30")}>
        <def.Component title={title} options={options} setOption={setOption} />
      </div>
    </motion.div>
  )
}

/** What follows the pointer while a widget is dragged. */
function Ghost({ id }: { id: string }) {
  const def = widgetOf(id)
  return (
    def && (
      <div className="flex h-full min-h-20 rotate-1 items-center gap-3 rounded-xl bg-popover/90 p-5 shadow-2xl ring-2 ring-primary/50 backdrop-blur-xl">
        <IconTile icon={def.icon} />
        <span className="heading text-lg">{t(def.title)}</span>
      </div>
    )
  )
}
