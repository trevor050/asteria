import '@wailsio/runtime'
import { mount } from 'svelte'
import './style.css'
import './lib/theme'
import App from './App.svelte'

const app = mount(App, { target: document.getElementById('app')! })

export default app
