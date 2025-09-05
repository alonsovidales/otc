import { useState } from 'react'

import './SignIn.css'

function SignIn({ onAuth }: { onAuth: (key: string) => Promise<void> }) {
  const [key, setKey] = useState("");

  return (
    <div className="sign_in_menu">
      <h1>Log In</h1>
      <form onSubmit={async (e) => { e.preventDefault(); await onAuth(key); }}>
        <div>
          <label>Password</label>
          <input type="password" onChange={(e)=>setKey(e.target.value)} />
        </div>
        <button type="submit">Submit</button>
      </form>
    </div>
  )
}

export default SignIn
