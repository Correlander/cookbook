// src/App.jsx
import React, { useState } from "react";
import Navbar from "./components/Navbar";

function App() {
  const [user, setUser] = useState(null);

  const handleLoginClick = () => {
    // TODO: open popup for Google login
  };

  return (
    <div className="min-h-screen w-full text-white bg-gray-900 flex flex-col">
      
      {/* Navbar */}
      <Navbar user={user} onLoginClick={handleLoginClick} />

      {/* Main content */}
      <main className="flex-1 flex flex-col items-center justify-center px-[clamp(1rem,2vw,4rem)] py-[clamp(1rem,2vw,4rem)] text-[clamp(1rem,1.5vw,2rem)]">
        <h1 className="text-center font-bold mb-6 text-[clamp(2rem,3vw,4rem)]">
          Welcome to the Cookbook!
        </h1>
        <p className="text-center max-w-[clamp(300px,80%,800px)]">
          This website lets you browse and search a collection of recipes.
          Use the database icon or search bar above to start exploring.
        </p>
      </main>

      {/* Optional footer */}
      <footer className="py-[clamp(0.5rem,1vw,1rem)] text-center text-[clamp(0.75rem,1vw,1rem)] text-gray-400">
        © 2025 Cookbook App
      </footer>
    </div>
  );
}

export default App;
