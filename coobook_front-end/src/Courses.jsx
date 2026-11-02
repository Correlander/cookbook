import { useEffect, useRef, useState } from 'react'
import useModalFocus from './useModalFocus.js'
import { moveStep } from './steps.ts'

export default function Courses({ onOpenRecipe }) {
  const [courses, setCourses] = useState([])
  const [enabled, setEnabled] = useState(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(null)
  const [openCourse, setOpenCourse] = useState(null)

  async function reload() {
    const response = await fetch('/api/courses?limit=100')
    if (response.status === 404) {
      setEnabled(false)
      return
    }
    if (!response.ok) throw new Error('Could not load courses.')
    setCourses(await response.json())
    setEnabled(true)
  }

  useEffect(() => {
    let active = true
    fetch('/api/courses?limit=100').then(async response => {
      if (response.status === 404) { if (active) setEnabled(false); return null }
      if (!response.ok) throw new Error('Could not load courses.')
      return response.json()
    }).then(items => { if (active && items) { setCourses(items); setEnabled(true) } })
      .catch(loadError => { if (active) { setEnabled(true); setError(loadError.message) } })
    return () => { active = false }
  }, [])

  if (enabled === false || enabled === null) return null
  return <section className="cookbook-dashboard course-list" aria-labelledby="courses-heading">
    <div className="section-heading"><div><span className="eyebrow">Learning together</span><h2 id="courses-heading">Courses</h2></div><button className="text-link" type="button" onClick={() => setEditing({})}>Create course</button></div>
    <p className="course-intro">Courses are authored lessons. Everyday recipe steps stay with their recipes.</p>
    {error && <p className="form-message" role="alert">{error}</p>}
    {courses.length ? <div className="cookbook-grid">{courses.map(course => <article className="cookbook-card" key={course.id}>
      <span className="recipe-card__category">{course.visibility} course · {course.lesson_count} lessons</span><h3>{course.title}</h3>{course.description&&<p>{course.description}</p>}<p className="course-owner">By {course.owner_name}</p>
      <button type="button" className="text-link" onClick={() => setOpenCourse(course)}>Open course</button>
      {course.owner_id&&<button type="button" className="text-link" onClick={() => setEditing(course)}>Edit course</button>}
    </article>)}</div> : <p className="cookbook-empty">No courses yet. Create one for a set of lessons.</p>}
    {editing&&<CourseForm course={editing.id?editing:null} onClose={() => setEditing(null)} onSaved={async () => { setEditing(null); await reload() }}/>}
    {openCourse&&<CourseDetail course={openCourse} onClose={() => setOpenCourse(null)} onEdit={() => { setEditing(openCourse); setOpenCourse(null) }} onOpenRecipe={onOpenRecipe} onChanged={reload}/>}
  </section>
}

export function PublicCourses() {
  const [courses, setCourses] = useState([])
  const [selected, setSelected] = useState(null)
  useEffect(() => {
    let active = true
    fetch('/api/public/courses?limit=24').then(async response => response.ok ? response.json() : [])
      .then(items => { if (active) setCourses(items) }).catch(() => { if (active) setCourses([]) })
    return () => { active = false }
  }, [])
  if (!courses.length) return null
  return <section className="cookbook-dashboard course-list" aria-labelledby="public-courses-heading">
    <div className="section-heading"><div><span className="eyebrow">Learn something new</span><h2 id="public-courses-heading">Public courses</h2></div></div>
    <div className="cookbook-grid">{courses.map(course => <article className="cookbook-card" key={course.id}><span className="recipe-card__category">{course.lesson_count} lessons</span><h3>{course.title}</h3>{course.description&&<p>{course.description}</p>}<p className="course-owner">By {course.owner_name}</p><button className="text-link" type="button" onClick={()=>setSelected(course)}>Open course</button></article>)}</div>
    {selected&&<CourseDetail course={selected} onClose={()=>setSelected(null)} onEdit={()=>{}} onChanged={async()=>{}}/>}
  </section>
}

function CourseForm({ course, onClose, onSaved }) {
  const dialogRef = useRef(null)
  useModalFocus(dialogRef, onClose)
  const [title, setTitle] = useState(course?.title || '')
  const [description, setDescription] = useState(course?.description || '')
  const [visibility, setVisibility] = useState(course?.visibility || 'private')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  async function submit(event) {
    event.preventDefault(); setSaving(true); setError('')
    try {
      const response = await fetch(course ? `/api/courses/${course.id}` : '/api/courses', { method: course ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title, description, visibility }) })
      if (!response.ok) throw new Error('Could not save this course. Check instance visibility settings and try again.')
      await onSaved()
    } catch (saveError) { setError(saveError.message) } finally { setSaving(false) }
  }

  return <div className="dialog-backdrop" role="presentation" onMouseDown={event => event.target === event.currentTarget && onClose()}><section ref={dialogRef} tabIndex={-1} className="signin-dialog" role="dialog" aria-modal="true" aria-labelledby="course-form-title">
    <button className="icon-button dialog-close" type="button" aria-label="Close course form" onClick={onClose}>×</button><span className="eyebrow">Courses</span><h2 id="course-form-title">{course?'Edit course':'Create a course'}</h2>
    <form onSubmit={submit}><label htmlFor="course-title">Title</label><input id="course-title" value={title} onChange={event=>setTitle(event.target.value)} maxLength="200" required/><label htmlFor="course-description">Description</label><textarea id="course-description" value={description} onChange={event=>setDescription(event.target.value)} maxLength="5000"/><label htmlFor="course-visibility">Visibility</label><select id="course-visibility" value={visibility} onChange={event=>setVisibility(event.target.value)}><option value="private">Private</option><option value="public">Public</option></select><p className="course-intro">Public courses can be read anonymously only when public browsing is enabled for this instance.</p><button className="button button--green" type="submit" disabled={saving}>{saving?'Saving…':'Save course'}</button></form>
    {error&&<p className="form-message" role="alert">{error}</p>}
  </section></div>
}

function CourseDetail({ course, onClose, onEdit, onOpenRecipe, onChanged }) {
  const dialogRef = useRef(null)
  useModalFocus(dialogRef, onClose)
  const [lessons, setLessons] = useState([])
  const [error, setError] = useState('')
  const [lessonOpen, setLessonOpen] = useState(false)
  const owner = Boolean(course.owner_id)
  const lessonsEndpoint = owner ? `/api/courses/${course.id}/lessons` : `/api/public/courses/${course.id}/lessons`

  async function reload() {
    const response = await fetch(lessonsEndpoint)
    if (!response.ok) throw new Error('Could not load course lessons.')
    setLessons(await response.json())
  }
  useEffect(() => { let active=true; fetch(lessonsEndpoint).then(async response=>{if(!response.ok)throw new Error('Could not load course lessons.');return response.json()}).then(items=>{if(active)setLessons(items)}).catch(loadError=>{if(active)setError(loadError.message)});return()=>{active=false} },[lessonsEndpoint])

  async function removeLesson(lesson) {
    if (!window.confirm(`Remove “${lesson.title}” from this course?`)) return
    const response = await fetch(`/api/courses/${course.id}/lessons/${lesson.id}`, { method: 'DELETE' })
    if (!response.ok) { setError('Could not remove this lesson.'); return }
    await reload(); await onChanged()
  }

  async function moveLesson(index, offset) {
    const ordered = moveStep(lessons, index, offset)
    if (ordered === lessons) return
    try {
      for (let position = 0; position < ordered.length; position += 1) {
        const response = await fetch(`/api/courses/${course.id}/lessons/${ordered[position].id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ position }) })
        if (!response.ok) throw new Error('Could not reorder these lessons.')
      }
      setLessons(ordered.map((lesson, position) => ({ ...lesson, position })))
      await onChanged()
    } catch (moveError) { setError(moveError.message); await reload() }
  }

  return <div className="dialog-backdrop" role="presentation" onMouseDown={event=>event.target===event.currentTarget&&onClose()}><section ref={dialogRef} tabIndex={-1} className="signin-dialog course-detail" role="dialog" aria-modal="true" aria-labelledby="course-title-heading">
    <button className="icon-button dialog-close" type="button" aria-label="Close course" onClick={onClose}>×</button><span className="eyebrow">{course.visibility} course · by {course.owner_name}</span><h2 id="course-title-heading">{course.title}</h2>{course.description&&<p>{course.description}</p>}
    {owner&&<div className="course-actions"><button className="text-link" type="button" onClick={onEdit}>Edit course</button><button className="text-link" type="button" onClick={()=>setLessonOpen(true)}>Add lesson</button><button className="text-link" type="button" onClick={async()=>{if(!window.confirm(`Delete “${course.title}” and all its lessons?`))return;const response=await fetch(`/api/courses/${course.id}`,{method:'DELETE'});if(!response.ok){setError('Could not delete this course.');return}onClose();await onChanged()}}>Delete course</button></div>}
    {error&&<p className="form-message" role="alert">{error}</p>}
    {lessons.length?<ol className="course-lessons">{lessons.map((lesson,index)=><li key={lesson.id}><span className="eyebrow">Lesson {index+1}</span><h3>{lesson.title}</h3>{lesson.content&&<p>{lesson.content}</p>}{lesson.recipe_id&&onOpenRecipe&&<button className="text-link" type="button" onClick={async()=>{const response=await fetch(`/api/recipes/${lesson.recipe_id}`);if(response.ok)onOpenRecipe?.(await response.json())}}>Open linked recipe</button>}{owner&&<div className="course-actions"><button className="text-link" type="button" disabled={index===0} onClick={()=>moveLesson(index,-1)}>Move up</button><button className="text-link" type="button" disabled={index===lessons.length-1} onClick={()=>moveLesson(index,1)}>Move down</button><button className="text-link" type="button" onClick={()=>setLessonOpen(lesson)}>Edit lesson</button><button className="text-link" type="button" onClick={()=>removeLesson(lesson)}>Remove lesson</button></div>}</li>)}</ol>:<p className="cookbook-empty">This course has no lessons yet.</p>}
    {lessonOpen&&<LessonForm course={course} lesson={lessonOpen===true?null:lessonOpen} position={typeof lessonOpen==='object'?lessonOpen.position:lessons.reduce((maximum,lesson)=>Math.max(maximum,lesson.position),-1)+1} onClose={()=>setLessonOpen(false)} onSaved={async()=>{setLessonOpen(false);await reload();await onChanged()}}/>}
  </section></div>
}

function LessonForm({ course, lesson, position, onClose, onSaved }) {
  const dialogRef=useRef(null); useModalFocus(dialogRef,onClose)
  const [title,setTitle]=useState(lesson?.title||''),[content,setContent]=useState(lesson?.content||''),[recipeID,setRecipeID]=useState(lesson?.recipe_id||''),[recipes,setRecipes]=useState([]),[error,setError]=useState(''),[saving,setSaving]=useState(false)
  useEffect(()=>{fetch('/api/recipes?limit=100').then(response=>response.ok?response.json():[]).then(setRecipes).catch(()=>setRecipes([]))},[])
  async function submit(event){event.preventDefault();setSaving(true);setError('');try{const response=await fetch(lesson?`/api/courses/${course.id}/lessons/${lesson.id}`:`/api/courses/${course.id}/lessons`,{method:lesson?'PATCH':'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title,content,recipe_id:recipeID,position})});if(!response.ok)throw new Error('Could not save this lesson.');await onSaved()}catch(saveError){setError(saveError.message)}finally{setSaving(false)}}
  return <div className="dialog-backdrop" role="presentation" onMouseDown={event=>event.target===event.currentTarget&&onClose()}><section ref={dialogRef} tabIndex={-1} className="signin-dialog" role="dialog" aria-modal="true" aria-labelledby="lesson-form-title"><button className="icon-button dialog-close" type="button" aria-label="Close lesson form" onClick={onClose}>×</button><span className="eyebrow">{lesson?'Edit':'New'} course lesson</span><h2 id="lesson-form-title">{lesson?'Edit lesson':'Add a lesson'}</h2><form onSubmit={submit}><label htmlFor="lesson-title">Lesson title</label><input id="lesson-title" value={title} onChange={event=>setTitle(event.target.value)} maxLength="200" required/><label htmlFor="lesson-content">Lesson content</label><textarea id="lesson-content" value={content} onChange={event=>setContent(event.target.value)} maxLength="20000"/><label htmlFor="lesson-recipe">Link an accessible recipe (optional)</label><select id="lesson-recipe" value={recipeID} onChange={event=>setRecipeID(event.target.value)}><option value="">No linked recipe</option>{recipes.map(recipe=><option key={recipe.id} value={recipe.id}>{recipe.title}</option>)}</select><button className="button button--green" type="submit" disabled={saving}>{saving?'Saving…':lesson?'Save lesson':'Add lesson'}</button></form>{error&&<p className="form-message" role="alert">{error}</p>}</section></div>
}
