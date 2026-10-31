import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export default function CookingMode({ recipe, onClose }) {
  const [step, setStep] = useState(0)
  const dialogRef = useRef(null)
  const [checked, setChecked] = useState(() => new Set())
  const steps = recipe.steps || []
  const ingredients = recipe.ingredients || []

  useEffect(() => {
    const previousFocus = document.activeElement
    dialogRef.current?.focus()
    function onKeyDown(event) {
      if (event.key === 'Escape') onClose()
      if (event.key === 'ArrowRight') setStep(current => Math.min(steps.length - 1, current + 1))
      if (event.key === 'ArrowLeft') setStep(current => Math.max(0, current - 1))
      if (event.key === 'Tab') {
        const controls = dialogRef.current?.querySelectorAll('button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])')
        if (!controls?.length) return
        const first = controls[0], last = controls[controls.length - 1]
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => { window.removeEventListener('keydown', onKeyDown); previousFocus?.focus?.() }
  }, [onClose, steps.length])

  function toggle(index) {
    setChecked(current => {
      const next = new Set(current)
      next.has(index) ? next.delete(index) : next.add(index)
      return next
    })
  }

  return createPortal(<div className="cooking-backdrop" role="presentation">
    <section ref={dialogRef} tabIndex={-1} data-cooking-mode className="cooking-mode" role="dialog" aria-modal="true" aria-labelledby="cooking-title">
      <header className="cooking-mode__header"><div><span className="eyebrow">Cooking mode</span><h2 id="cooking-title">{recipe.title}</h2></div><button className="icon-button" type="button" onClick={onClose} aria-label="Close cooking mode">×</button></header>
      <div className="cooking-mode__layout"><section aria-labelledby="cooking-ingredients"><h3 id="cooking-ingredients">Ingredients</h3><ul className="cooking-ingredients">{ingredients.map((item, index) => <li key={`${item.name}-${index}`}><label><input type="checkbox" checked={checked.has(index)} onChange={() => toggle(index)}/><span className={checked.has(index) ? 'is-checked' : ''}>{[item.quantity, item.name].filter(Boolean).join(' ')}{item.notes && <small> — {item.notes}</small>}</span></label></li>)}</ul></section>
        <section className="cooking-step" aria-labelledby="step-heading"><span className="eyebrow">Step {step + 1} of {steps.length}</span><h3 id="step-heading" aria-live="polite" aria-atomic="true">{steps[step]?.text}</h3><nav className="cooking-controls" aria-label="Cooking steps"><button type="button" className="button button--light" disabled={step===0} onClick={()=>setStep(value=>Math.max(0,value-1))}>Previous</button><button type="button" className="button button--green" disabled={step===steps.length-1} onClick={()=>setStep(value=>Math.min(steps.length-1,value+1))}>{step===steps.length-1?'Finished':'Next step'}</button></nav><p className="cooking-help">Use the arrow keys to move between steps. Press Escape to close.</p></section></div>
      <p className="sr-only" role="status" aria-live="polite">Step {step + 1} of {steps.length}</p>
    </section>
  </div>, document.body)
}
