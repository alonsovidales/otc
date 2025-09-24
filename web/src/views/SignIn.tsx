import { useState } from 'react'

import './SignIn.css'

function SignIn({ onAuth }: { onAuth: (key: string) => Promise<void> }) {
  const [key, setKey] = useState("");

  return (
    <section className="sf-section" style={{ width: 400, margin: "auto" }}>
      <form onSubmit={async (e) => { e.preventDefault(); await onAuth(key); }}>
        <div className="sf-row">
        <h3>Password</h3>
          <input id="sf-old" className="sf-input" type="password" onChange={(e)=>setKey(e.target.value)} />
        </div>
        <button className="sf-btn">
          Log In
        </button>
      </form>
    </section>
  )
}

export default SignIn
