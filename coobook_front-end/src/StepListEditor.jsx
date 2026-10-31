import { createStep, moveStep } from './steps.js'

export default function StepListEditor({ steps, onChange }) {
  function updateStep(id, text) {
    onChange(current => current.map(step => step.id === id ? { ...step, text } : step))
  }

  function removeStep(id) {
    onChange(current => current.length === 1
      ? [{ ...current[0], text: '' }]
      : current.filter(step => step.id !== id))
  }

  return <fieldset className="step-editor">
    <legend>Instructions</legend>
    {steps.map((step, index) => <div className="step-editor__row" key={step.id}>
      <label htmlFor={`recipe-step-${step.id}`}>Step {index + 1}</label>
      <textarea id={`recipe-step-${step.id}`} value={step.text} onChange={event => updateStep(step.id, event.target.value)} maxLength={5000} required />
      <div className="step-editor__controls" aria-label={`Controls for step ${index + 1}`}>
        <button type="button" className="text-link" disabled={index === 0} aria-label={`Move step ${index + 1} up`} onClick={() => onChange(current => moveStep(current, index, -1))}>Move up</button>
        <button type="button" className="text-link" disabled={index === steps.length - 1} aria-label={`Move step ${index + 1} down`} onClick={() => onChange(current => moveStep(current, index, 1))}>Move down</button>
        <button type="button" className="text-link" disabled={steps.length === 1} aria-label={`Remove step ${index + 1}`} onClick={() => removeStep(step.id)}>Remove</button>
      </div>
    </div>)}
    <button type="button" className="text-link step-editor__add" onClick={() => onChange(current => [...current, createStep()])}>Add an instruction step</button>
  </fieldset>
}
