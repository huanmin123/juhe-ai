<template>
  <div ref="pageRef" class="login-page" @pointerenter="pointerEnter" @pointerleave="pointerLeave" @pointermove="pointerMove">
    <div class="glow g1" aria-hidden="true" />
    <div class="glow g2" aria-hidden="true" />
    <div class="scene">
      <section class="masthead" aria-labelledby="login-title">
        <h1 id="login-title">
          <span class="line l1"><span>进入模型</span></span>
          <span class="line l2"><span class="thin">澄明之地</span></span>
        </h1>
        <svg class="brushline" viewBox="0 0 300 26" aria-hidden="true">
          <path class="lead" d="M4 15 C70 9 150 9 240 15" />
          <path class="tail" d="M10 21 C70 16 150 16 234 20" />
          <circle class="dot" cx="242" cy="15" r="2.2" />
        </svg>
      </section>

      <div class="body">
        <aside class="left-note" aria-label="可接入的模型">
          <div class="hub">
            <div class="ring" aria-hidden="true" />
            <div class="core" :title="appBrand.appName">
              <svg viewBox="0 0 256 256" aria-hidden="true"><path fill="#ffffff" d="M208 144a15.78 15.78 0 0 1-10.42 14.94L146 178l-19 51.62a15.92 15.92 0 0 1-29.88 0L78 178l-51.62-19a15.92 15.92 0 0 1 0-29.88L78 110l19-51.62a15.92 15.92 0 0 1 29.88 0L146 110l51.62 19A15.78 15.78 0 0 1 208 144m-56-96h16v16a8 8 0 0 0 16 0V48h16a8 8 0 0 0 0-16h-16V16a8 8 0 0 0-16 0v16h-16a8 8 0 0 0 0 16m88 32h-8v-8a8 8 0 0 0-16 0v8h-8a8 8 0 0 0 0 16h8v8a8 8 0 0 0 16 0v-8h8a8 8 0 0 0 0-16" /></svg>
            </div>
            <div class="orbit">
          <div class="node" style="left:90%;top:50%;transform:translate(-50%,-50%)" title="OpenAI"><svg viewBox="0 0 256 260" aria-hidden="true"><path fill="#000000" d="M239.184 106.203a64.716 64.716 0 0 0-5.576-53.103C219.452 28.459 191 15.784 163.213 21.74A65.586 65.586 0 0 0 52.096 45.22a64.716 64.716 0 0 0-43.23 31.36c-14.31 24.602-11.061 55.634 8.033 76.74a64.665 64.665 0 0 0 5.525 53.102c14.174 24.65 42.644 37.324 70.446 31.36a64.72 64.72 0 0 0 48.754 21.744c28.481.025 53.714-18.361 62.414-45.481a64.767 64.767 0 0 0 43.229-31.36c14.137-24.558 10.875-55.423-8.083-76.483Zm-97.56 136.338a48.397 48.397 0 0 1-31.105-11.255l1.535-.87 51.67-29.825a8.595 8.595 0 0 0 4.247-7.367v-72.85l21.845 12.636c.218.111.37.32.409.563v60.367c-.056 26.818-21.783 48.545-48.601 48.601Zm-104.466-44.61a48.345 48.345 0 0 1-5.781-32.589l1.534.921 51.722 29.826a8.339 8.339 0 0 0 8.441 0l63.181-36.425v25.221a.87.87 0 0 1-.358.665l-52.335 30.184c-23.257 13.398-52.97 5.431-66.404-17.803ZM23.549 85.38a48.499 48.499 0 0 1 25.58-21.333v61.39a8.288 8.288 0 0 0 4.195 7.316l62.874 36.272-21.845 12.636a.819.819 0 0 1-.767 0L41.353 151.53c-23.211-13.454-31.171-43.144-17.804-66.405v.256Zm179.466 41.695-63.08-36.63L161.73 77.86a.819.819 0 0 1 .768 0l52.233 30.184a48.6 48.6 0 0 1-7.316 87.635v-61.391a8.544 8.544 0 0 0-4.4-7.213Zm21.742-32.69-1.535-.922-51.619-30.081a8.39 8.39 0 0 0-8.492 0L99.98 99.808V74.587a.716.716 0 0 1 .307-.665l52.233-30.133a48.652 48.652 0 0 1 72.236 50.391v.205ZM88.061 139.097l-21.845-12.585a.87.87 0 0 1-.41-.614V65.685a48.652 48.652 0 0 1 79.757-37.346l-1.535.87-51.67 29.825a8.595 8.595 0 0 0-4.246 7.367l-.051 72.697Zm11.868-25.58 28.138-16.217 28.188 16.218v32.434l-28.086 16.218-28.188-16.218-.052-32.434Z"></path></svg></div>
          <div class="node" style="left:78.28%;top:78.28%;transform:translate(-50%,-50%)" title="Claude"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="#D97757" d="m4.7144 15.9555 4.7174-2.6471.079-.2307-.079-.1275h-.2307l-.7893-.0486-2.6956-.0729-2.3375-.0971-2.2646-.1214-.5707-.1215-.5343-.7042.0546-.3522.4797-.3218.686.0608 1.5179.1032 2.2767.1578 1.6514.0972 2.4468.255h.3886l.0546-.1579-.1336-.0971-.1032-.0972L6.973 9.8356l-2.55-1.6879-1.3356-.9714-.7225-.4918-.3643-.4614-.1578-1.0078.6557-.7225.8803.0607.2246.0607.8925.686 1.9064 1.4754 2.4893 1.8336.3643.3035.1457-.1032.0182-.0728-.164-.2733-1.3539-2.4467-1.476-2.4893-.6435-1.032-.17-.6194c-.0607-.255-.1032-.4674-.1032-.7285L6.287.1335 6.6997 0l.9957.1336.419.3642.6192 1.4147 1.0018 2.2282 1.5543 3.0296.4553.8985.2429.8318.091.255h.1579v-.1457l.1275-1.706.2368-2.0947.2307-2.6957.0789-.7589.3764-.9107.7468-.4918.5828.2793.4797.686-.0668.4433-.2853 1.8517-.5586 2.9021-.3643 1.9429h.2125l.2429-.2429.9835-1.3053 1.6514-2.0643.7286-.8196.85-.9046.5464-.4311h1.0321l.759 1.1293-.34 1.1657-1.0625 1.3478-.8804 1.1414-1.2628 1.7-.7893 1.36.0729.1093.1882-.0183 2.8535-.607 1.5421-.2794 1.8396-.3157.8318.3886.091.3946-.3278.8075-1.967.4857-2.3072.4614-3.4364.8136-.0425.0304.0486.0607 1.5482.1457.6618.0364h1.621l3.0175.2247.7892.522.4736.6376-.079.4857-1.2142.6193-1.6393-.3886-3.825-.9107-1.3113-.3279h-.1822v.1093l1.0929 1.0686 2.0035 1.8092 2.5075 2.3314.1275.5768-.3218.4554-.34-.0486-2.2039-1.6575-.85-.7468-1.9246-1.621h-.1275v.17l.4432.6496 2.3436 3.5214.1214 1.0807-.17.3521-.6071.2125-.6679-.1214-1.3721-1.9246L14.38 17.959l-1.1414-1.9428-.1397.079-.674 7.2552-.3156.3703-.7286.2793-.6071-.4614-.3218-.7468.3218-1.4753.3886-1.9246.3157-1.53.2853-1.9004.17-.6314-.0121-.0425-.1397.0182-1.4328 1.9672-2.1796 2.9446-1.7243 1.8456-.4128.164-.7164-.3704.0667-.6618.4008-.5889 2.386-3.0357 1.4389-1.882.929-1.0868-.0062-.1579h-.0546l-6.3385 4.1164-1.1293.1457-.4857-.4554.0608-.7467.2307-.2429 1.9064-1.3114Z"></path></svg></div>
          <div class="node" style="left:50%;top:90%;transform:translate(-50%,-50%)" title="Gemini"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="#8E75B2" d="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81"></path></svg></div>
          <div class="node" style="left:21.72%;top:78.28%;transform:translate(-50%,-50%)" title="DeepSeek"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="#4D6BFE" d="M23.748 4.482c-.254-.124-.364.113-.512.234-.051.039-.094.09-.137.136-.372.397-.806.657-1.373.626-.829-.046-1.537.214-2.163.848-.133-.782-.575-1.248-1.247-1.548-.352-.156-.708-.311-.955-.65-.172-.241-.219-.51-.305-.774-.055-.16-.11-.323-.293-.35-.2-.031-.278.136-.356.276-.313.572-.434 1.202-.422 1.84.027 1.436.633 2.58 1.838 3.393.137.093.172.187.129.323-.082.28-.18.552-.266.833-.055.179-.137.217-.329.14a5.526 5.526 0 0 1-1.736-1.18c-.857-.828-1.631-1.742-2.597-2.458a11.365 11.365 0 0 0-.689-.471c-.985-.957.13-1.743.388-1.836.27-.098.093-.432-.779-.428-.872.004-1.67.295-2.687.684a3.055 3.055 0 0 1-.465.137 9.597 9.597 0 0 0-2.883-.102c-1.885.21-3.39 1.102-4.497 2.623C.082 8.606-.231 10.684.152 12.85c.403 2.284 1.569 4.175 3.36 5.653 1.858 1.533 3.997 2.284 6.438 2.14 1.482-.085 3.133-.284 4.994-1.86.47.234.962.327 1.78.397.63.059 1.236-.03 1.705-.128.735-.156.684-.837.419-.961-2.155-1.004-1.682-.595-2.113-.926 1.096-1.296 2.746-2.642 3.392-7.003.05-.347.007-.565 0-.845-.004-.17.035-.237.23-.256a4.173 4.173 0 0 0 1.545-.475c1.396-.763 1.96-2.015 2.093-3.517.02-.23-.004-.467-.247-.588zM11.581 18c-2.089-1.642-3.102-2.183-3.52-2.16-.392.024-.321.471-.235.763.09.288.207.486.371.739.114.167.192.416-.113.603-.673.416-1.842-.14-1.897-.167-1.361-.802-2.5-1.86-3.301-3.307-.774-1.393-1.224-2.887-1.298-4.482-.02-.386.093-.522.477-.592a4.696 4.696 0 0 1 1.529-.039c2.132.312 3.946 1.265 5.468 2.774.868.86 1.525 1.887 2.202 2.891.72 1.066 1.494 2.082 2.48 2.914.348.292.625.514.891.677-.802.09-2.14.11-3.054-.614zm1-6.44a.306.306 0 0 1 .415-.287.302.302 0 0 1 .2.288.306.306 0 0 1-.31.307.303.303 0 0 1-.304-.308zm3.11 1.596c-.2.081-.399.151-.59.16a1.245 1.245 0 0 1-.798-.254c-.274-.23-.47-.358-.552-.758a1.73 1.73 0 0 1 .016-.588c.07-.327-.008-.537-.239-.727-.187-.156-.426-.199-.688-.199a.559.559 0 0 1-.254-.078.253.253 0 0 1-.114-.358c.028-.054.16-.186.192-.21.356-.202.767-.136 1.146.016.352.144.618.408 1.001.782.391.451.462.576.685.914.176.265.336.537.445.848.067.195-.019.354-.25.452z"></path></svg></div>
          <div class="node" style="left:10%;top:50%;transform:translate(-50%,-50%)" title="Kimi"><svg viewBox="0 0 512 512" aria-hidden="true"><path d="M503 114.333v280c0 60.711-49.29 110-110 110H113c-60.711 0-110-49.289-110-110v-280c0-60.71 49.289-110 110-110h280c60.71 0 110 49.29 110 110z"></path><path d="M342.065 189.759c1.886-2.42 3.541-4.63 5.289-6.77.81-1.007.74-1.771-.046-2.824-7.58-9.965-8.298-21.028-3.935-32.254 3.275-8.448 10.52-12.406 19.373-13.25 5.52-.521 10.936.046 15.959 2.73 6.596 3.53 10.438 8.912 11.688 16.341.995 5.926.81 11.712-.868 17.452-2.974 10.161-10.277 15.427-20.287 16.758-8.31 1.11-16.734 1.25-25.113 1.817-.648.046-1.308 0-2.06 0z" fill="#027aff"></path><path d="M321.512 144.254h-50.064l-39.637 90.384h-56.036v-89.99H131v232.868h44.787v-98.103h78.973c13.598 0 26.015-7.927 31.744-20.252v118.355h44.787v-98.103c0-23.342-18.239-42.97-41.523-44.671v-.116h-24.593a45.577 45.577 0 0026.884-24.534l29.453-65.838z" fill="#fff"></path></svg></div>
          <div class="node" style="left:21.72%;top:21.72%;transform:translate(-50%,-50%)" title="Qwen"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="#615CED" fill-rule="evenodd" d="M12.604 1.34c.393.69.784 1.382 1.174 2.075a.18.18 0 00.157.091h5.552c.174 0 .322.11.446.327l1.454 2.57c.19.337.24.478.024.837-.26.43-.513.864-.76 1.3l-.367.658c-.106.196-.223.28-.04.512l2.652 4.637c.172.301.111.494-.043.77-.437.785-.882 1.564-1.335 2.34-.159.272-.352.375-.68.37-.777-.016-1.552-.01-2.327.016a.099.099 0 00-.081.05 575.097 575.097 0 01-2.705 4.74c-.169.293-.38.363-.725.364-.997.003-2.002.004-3.017.002a.537.537 0 01-.465-.271l-1.335-2.323a.09.09 0 00-.083-.049H4.982c-.285.03-.553-.001-.805-.092l-1.603-2.77a.543.543 0 01-.002-.54l1.207-2.12a.198.198 0 000-.197 550.951 550.951 0 01-1.875-3.272l-.79-1.395c-.16-.31-.173-.496.095-.965.465-.813.927-1.625 1.387-2.436.132-.234.304-.334.584-.335a338.3 338.3 0 012.589-.001.124.124 0 00.107-.063l2.806-4.895a.488.488 0 01.422-.246c.524-.001 1.053 0 1.583-.006L11.704 1c.341-.003.724.032.9.34zm-3.432.403a.06.06 0 00-.052.03L6.254 6.788a.157.157 0 01-.135.078H3.253c-.056 0-.07.025-.041.074l5.81 10.156c.025.042.013.062-.034.063l-2.795.015a.218.218 0 00-.2.116l-1.32 2.31c-.044.078-.021.118.068.118l5.716.008c.046 0 .08.02.104.061l1.403 2.454c.046.081.092.082.139 0l5.006-8.76.783-1.382a.055.055 0 01.096 0l1.424 2.53a.122.122 0 00.107.062l2.763-.02a.04.04 0 00.035-.02.041.041 0 000-.04l-2.9-5.086a.108.108 0 010-.113l.293-.507 1.12-1.977c.024-.041.012-.062-.035-.062H9.2c-.059 0-.073-.026-.043-.077l1.434-2.505a.107.107 0 000-.114L9.225 1.774a.06.06 0 00-.053-.031zm6.29 8.02c.046 0 .058.02.034.06l-.832 1.465-2.613 4.585a.056.056 0 01-.05.029.058.058 0 01-.05-.029L8.498 9.841c-.02-.034-.01-.052.028-.054l.216-.012 6.722-.012z"></path></svg></div>
          <div class="node" style="left:50%;top:10%;transform:translate(-50%,-50%)" title="豆包"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5.31 15.756c.172-3.75 1.883-5.999 2.549-6.739-3.26 2.058-5.425 5.658-6.358 8.308v1.12C1.501 21.513 4.226 24 7.59 24a6.59 6.59 0 002.2-.375c.353-.12.7-.248 1.039-.378.913-.899 1.65-1.91 2.243-2.992-4.877 2.431-7.974.072-7.763-4.5l.002.001z" fill="#1E37FC"></path><path d="M22.57 10.283c-1.212-.901-4.109-2.404-7.397-2.8.295 3.792.093 8.766-2.1 12.773a12.782 12.782 0 01-2.244 2.992c3.764-1.448 6.746-3.457 8.596-5.219 2.82-2.683 3.353-5.178 3.361-6.66a2.737 2.737 0 00-.216-1.084v-.002z" fill="#37E1BE"></path><path d="M14.303 1.867C12.955.7 11.248 0 9.39 0 7.532 0 5.883.677 4.545 1.807 2.791 3.29 1.627 5.557 1.5 8.125v9.201c.932-2.65 3.097-6.25 6.357-8.307.5-.318 1.025-.595 1.569-.829 1.883-.801 3.878-.932 5.746-.706-.222-2.83-.718-5.002-.87-5.617h.001z" fill="#A569FF"></path><path d="M17.305 4.961a199.47 199.47 0 01-1.08-1.094c-.202-.213-.398-.419-.586-.622l-1.333-1.378c.151.615.648 2.786.869 5.617 3.288.395 6.185 1.898 7.396 2.8-1.306-1.275-3.475-3.487-5.266-5.323z" fill="#1E37FC"></path></svg></div>
          <div class="node" style="left:78.28%;top:21.72%;transform:translate(-50%,-50%)" title="GLM"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="#3859FF" fill-rule="nonzero" d="M11.991 23.503a.24.24 0 00-.244.248.24.24 0 00.244.249.24.24 0 00.245-.249.24.24 0 00-.22-.247l-.025-.001zM9.671 5.365a1.697 1.697 0 011.099 2.132l-.071.172-.016.04-.018.054c-.07.16-.104.32-.104.498-.035.71.47 1.279 1.186 1.314h.366c1.309.053 2.338 1.173 2.286 2.523-.052 1.332-1.152 2.38-2.478 2.327h-.174c-.715.018-1.274.64-1.239 1.368 0 .124.018.23.053.337.209.373.54.658.96.8.75.23 1.517-.125 1.9-.782l.018-.035c.402-.64 1.17-.96 1.92-.711.854.284 1.378 1.226 1.099 2.167a1.661 1.661 0 01-2.077 1.102 1.711 1.711 0 01-.907-.711l-.017-.035c-.2-.323-.463-.58-.851-.711l-.056-.018a1.646 1.646 0 00-1.954.746 1.66 1.66 0 01-1.065.764 1.677 1.677 0 01-1.989-1.279c-.209-.906.332-1.83 1.257-2.043a1.51 1.51 0 01.296-.035h.018c.68-.071 1.151-.622 1.116-1.333a1.307 1.307 0 00-.227-.693 2.515 2.515 0 01-.366-1.403 2.39 2.39 0 01.366-1.208c.14-.195.21-.444.227-.693.018-.71-.506-1.261-1.186-1.332l-.07-.018a1.43 1.43 0 01-.299-.07l-.05-.019a1.7 1.7 0 01-1.047-2.114 1.68 1.68 0 012.094-1.101zm-5.575 10.11c.26-.264.639-.367.994-.27.355.096.633.379.728.74.095.362-.007.748-.267 1.013-.402.41-1.053.41-1.455 0a1.062 1.062 0 010-1.482zm14.845-.294c.359-.09.738.024.992.297.254.274.344.665.237 1.025-.107.36-.396.634-.756.718-.551.128-1.1-.22-1.23-.781a1.05 1.05 0 01.757-1.26zm-.064-4.39c.314.32.49.753.49 1.206 0 .452-.176.886-.49 1.206-.315.32-.74.5-1.185.5-.444 0-.87-.18-1.184-.5a1.727 1.727 0 010-2.412 1.654 1.654 0 012.369 0zm-11.243.163c.364.484.447 1.128.218 1.691a1.665 1.665 0 01-2.188.923c-.855-.36-1.26-1.358-.907-2.228a1.68 1.68 0 011.33-1.038c.593-.08 1.183.169 1.547.652zm11.545-4.221c.368 0 .708.2.892.524.184.324.184.724 0 1.048a1.026 1.026 0 01-.892.524c-.568 0-1.03-.47-1.03-1.048 0-.579.462-1.048 1.03-1.048zm-14.358 0c.368 0 .707.2.891.524.184.324.184.724 0 1.048a1.026 1.026 0 01-.891.524c-.569 0-1.03-.47-1.03-1.048 0-.579.461-1.048 1.03-1.048zm10.031-1.475c.925 0 1.675.764 1.675 1.706s-.75 1.705-1.675 1.705-1.674-.763-1.674-1.705c0-.942.75-1.706 1.674-1.706zm-2.626-.684c.362-.082.653-.356.761-.718a1.062 1.062 0 00-.238-1.028 1.017 1.017 0 00-.996-.294c-.547.14-.881.7-.752 1.257.13.558.675.907 1.225.783zm0 16.876c.359-.087.644-.36.75-.72a1.062 1.062 0 00-.237-1.019 1.018 1.018 0 00-.985-.301 1.037 1.037 0 00-.762.717c-.108.361-.017.754.239 1.028.245.263.606.377.953.305l.043-.01zM17.19 3.5a.631.631 0 00.628-.64c0-.355-.279-.64-.628-.64a.631.631 0 00-.628.64c0 .355.28.64.628.64zm-10.38 0a.631.631 0 00.628-.64c0-.355-.28-.64-.628-.64a.631.631 0 00-.628.64c0 .355.279.64.628.64zm-5.182 7.852a.631.631 0 00-.628.64c0 .354.28.639.628.639a.63.63 0 00.627-.606l.001-.034a.62.62 0 00-.628-.64zm5.182 9.13a.631.631 0 00-.628.64c0 .355.279.64.628.64a.631.631 0 00.628-.64c0-.355-.28-.64-.628-.64zm10.38.018a.631.631 0 00-.628.64c0 .355.28.64.628.64a.631.631 0 00.628-.64c0-.355-.279-.64-.628-.64zm5.182-9.148a.631.631 0 00-.628.64c0 .354.279.639.628.639a.631.631 0 00.628-.64c0-.355-.28-.64-.628-.64zm-.384-4.992a.24.24 0 00.244-.249.24.24 0 00-.244-.249.24.24 0 00-.244.249c0 .142.122.249.244.249zM11.991.497a.24.24 0 00.245-.248A.24.24 0 0011.99 0a.24.24 0 00-.244.249c0 .133.108.236.223.247l.021.001zM2.011 6.36a.24.24 0 00.245-.249.24.24 0 00-.244-.249.24.24 0 00-.244.249.24.24 0 00.244.249zm0 11.263a.24.24 0 00-.243.248.24.24 0 00.244.249.24.24 0 00.244-.249.252.252 0 00-.244-.248zm19.995-.018a.24.24 0 00-.245.248.24.24 0 00.245.25.24.24 0 00.244-.25.252.252 0 00-.244-.248z"></path></svg></div>
            </div>
          </div>
        </aside>

        <main class="auth-wrap" aria-label="登录">
          <div class="auth-icons">
            <span class="brand-mark" :title="appBrand.appName">
              <svg viewBox="0 0 256 256" aria-hidden="true"><path fill="#ffffff" d="M208 144a15.78 15.78 0 0 1-10.42 14.94L146 178l-19 51.62a15.92 15.92 0 0 1-29.88 0L78 178l-51.62-19a15.92 15.92 0 0 1 0-29.88L78 110l19-51.62a15.92 15.92 0 0 1 29.88 0L146 110l51.62 19A15.78 15.78 0 0 1 208 144m-56-96h16v16a8 8 0 0 0 16 0V48h16a8 8 0 0 0 0-16h-16V16a8 8 0 0 0-16 0v16h-16a8 8 0 0 0 0 16m88 32h-8v-8a8 8 0 0 0-16 0v8h-8a8 8 0 0 0 0 16h8v8a8 8 0 0 0 16 0v-8h8a8 8 0 0 0 0-16" /></svg>
            </span>
            <span class="login-word">登录·{{ appBrand.appName.replace(/\s+/g, '') }}</span>
          </div>

          <form class="login-form" novalidate @submit.prevent="handleLogin">
            <div class="field" :class="{ 'has-error': errors.username }">
              <div class="field-row"><label class="field-label" for="username">用户名</label><span class="field-hint">REQUIRED</span></div>
              <input id="username" v-model="form.username" class="input" autocomplete="username" placeholder="输入用户名" @blur="validate('username')" @input="errors.username = false" />
              <p class="field-error">请输入用户名</p>
            </div>
            <div class="field" :class="{ 'has-error': errors.password }">
              <div class="field-row"><label class="field-label" for="password">密码</label></div>
              <div class="input-wrap">
                <input id="password" v-model="form.password" class="input has-toggle" autocomplete="current-password" :type="passwordVisible ? 'text' : 'password'" placeholder="输入密码" @blur="validate('password')" @input="errors.password = false" />
                <button class="input-toggle" type="button" :aria-label="passwordVisible ? '隐藏密码' : '显示密码'" :title="passwordVisible ? '隐藏密码' : '显示密码'" @click="passwordVisible = !passwordVisible">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path v-if="!passwordVisible" d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z" /><circle v-if="!passwordVisible" cx="12" cy="12" r="2.5" /><path v-else d="M3 3l18 18M10.6 6.2A11 11 0 0 1 12 6c6 0 9.5 6 9.5 6a17 17 0 0 1-3.2 3.7M6.2 6.2C3.8 8.1 2.5 12 2.5 12a17 17 0 0 0 5.1 4.7A10 10 0 0 0 12 18c.5 0 1-.1 1.4-.1" /></svg>
                </button>
              </div>
              <p class="field-error">请输入密码</p>
            </div>
            <div v-if="captchaRequired" class="field" :class="{ 'has-error': errors.captcha }">
              <div class="field-row"><label class="field-label" for="captcha">验证码</label><span class="field-hint">点击右侧刷新</span></div>
              <div class="captcha-row">
                <input id="captcha" v-model="form.captchaCode" class="input" autocomplete="off" inputmode="text" maxlength="6" placeholder="输入验证码" @blur="validate('captcha')" @input="errors.captcha = false" />
                <button class="captcha-button" type="button" :disabled="captchaLoading" :aria-busy="captchaLoading" aria-label="刷新验证码" title="刷新验证码" @click="refreshCaptcha">
                  <img v-if="captcha?.image" :src="captcha.image" alt="验证码图片" />
                  <span v-else>{{ captchaLoading ? '加载中' : captchaFallbackCode }}</span>
                </button>
              </div>
              <p class="field-error">请输入验证码</p>
            </div>
            <button class="submit-button focus-ring" type="submit" :disabled="loading"><span v-if="loading" class="spinner" />{{ loading ? '正在进入' : '进入澄明' }}</button>
            <p class="type-note" aria-live="polite"><span v-for="(char, i) in typedChars" :key="i" class="type-char" :class="{ in: i < typedIndex, out: i >= typedIndex }">{{ char }}</span><span class="type-caret" aria-hidden="true" /></p>
          </form>
        </main>
      </div>

      <div class="shoreline" aria-hidden="true">
        <div class="wave-track running">
          <div v-for="n in 3" :key="n" class="wave-group">
            <svg viewBox="0 0 600 84" preserveAspectRatio="none">
              <path class="w1" d="M0 48 C100 36 200 60 300 48 S500 36 600 48" />
              <path class="w2" d="M0 62 C100 52 200 72 300 62 S500 52 600 62" />
              <path class="w3" d="M0 74 C100 66 200 80 300 74 S500 66 600 74" />
            </svg>
          </div>
        </div>
        <div class="boat">
          <svg viewBox="0 0 92 44">
            <g class="boat-body">
              <path d="M6 30 Q46 42 86 30 L80 26 Q46 34 12 26 Z" fill="var(--fg)" opacity=".85" />
              <g class="figure">
                <ellipse cx="48" cy="20" rx="4.6" ry="5.4" fill="var(--fg)" />
                <path d="M40 28 Q48 16 56 28 L53 32 L43 32 Z" fill="var(--fg)" />
                <line x1="62" y1="8" x2="56" y2="34" stroke="var(--fg)" stroke-width="1.5" stroke-linecap="round" opacity=".7" />
              </g>
            </g>
          </svg>
        </div>
      </div>
      <div ref="fishLayer" class="fish-layer" aria-hidden="true" />
    </div>
    <div v-if="toast" class="toast" role="status" aria-live="polite">{{ toast }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { loadCaptcha, login } from '@/composables/useAuth'
import { appBrand, loadAppBrandSettings } from '@/composables/useAppBrand'
import { getPreferredEntryPath } from '@/composables/useMenuMode'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { CaptchaChallengeSummary } from '@/types/domain'

const router = useRouter()
const route = useRoute()
const pageRef = ref<HTMLElement | null>(null)
const fishLayer = ref<HTMLElement | null>(null)
const loading = ref(false)
const captchaLoading = ref(false)
const captcha = ref<CaptchaChallengeSummary>()
const captchaFallbackCode = ref('K7Q2')
const passwordVisible = ref(false)
const toast = ref('')
const form = reactive({ username: '', password: '', captchaCode: '' })
const errors = reactive({ username: false, password: false, captcha: false })
const captchaRequired = computed(() => captcha.value?.required !== false)
const typedChars = [...'一个入口聚合市面主流大模型，无缝调用，告别频繁切换。']
const typedIndex = ref(-1)
const fishTimers: number[] = []
const reduceMotion = typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
const FISH_SVG = '<svg viewBox="0 0 44 22" fill="none" aria-hidden="true"><path d="M40 11 32.5 4.2l2.8 6.8-2.8 6.8L40 11Z" fill="currentColor"/><path d="M4 11C4 5.6 11 3 20 3c9 0 16 2.8 16 8 0 5.2-7 8-16 8-9 0-16-2.6-16-8Z" fill="currentColor"/><circle cx="11" cy="9" r="1.5" style="fill:var(--surface-strong)"/></svg>'

let rect: DOMRect | undefined
let light: HTMLElement | null = null
let frame = 0
let point: { x: number; y: number } | undefined
let toastTimer = 0
let typeTimer = 0
let fishScheduleTimer = 0

function pointerEnter() {
  rect = pageRef.value?.getBoundingClientRect()
  light = pageRef.value?.querySelector('.g2') as HTMLElement | null
}
function pointerLeave() {
  light = null
}
function pointerMove(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  if (!rect) pointerEnter()
  if (!rect) return
  point = { x: event.clientX - rect.left, y: event.clientY - rect.top }
  if (!frame) {
    frame = requestAnimationFrame(() => {
      frame = 0
      if (light && point) light.style.transform = 'translate3d(' + Math.round(point.x - 140) + 'px,' + Math.round(point.y - 140) + 'px,0)'
    })
  }
}

function validate(field: 'username' | 'password' | 'captcha') {
  const value = field === 'username' ? form.username : field === 'password' ? form.password : form.captchaCode
  errors[field] = !value.trim()
  return !errors[field]
}

function showToast(value: string) {
  toast.value = value
  window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { toast.value = '' }, 2400)
}

function randomCaptcha() {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'
  return Array.from({ length: 4 }, () => chars[Math.floor(Math.random() * chars.length)]).join('')
}

async function handleLogin() {
  if (loading.value) return
  const valid = validate('username') && validate('password') && (!captchaRequired.value || validate('captcha'))
  if (!valid) {
    showToast('请补全登录信息后继续。')
    return
  }
  if (/\s/.test(form.username) || /\s/.test(form.password)) {
    showToast('用户名和密码不能包含空格')
    return
  }
  if (captchaRequired.value && !captcha.value?.captchaId) {
    showToast('验证码未加载，请刷新验证码')
    return
  }
  loading.value = true
  try {
    const user = await login({
      username: form.username.trim(),
      password: form.password,
      ...(captchaRequired.value ? { captchaId: captcha.value?.captchaId, captchaCode: form.captchaCode.trim() } : {})
    })
    if (user.mustChangePassword) showToast('当前账户使用初始密码，请先完成修改')
    const redirect = resolveRedirect(user)
    if (isHelp(redirect) || isOAuth(redirect)) {
      window.location.assign(redirect)
      return
    }
    await router.replace(redirect)
  } catch (error) {
    console.error(error)
    showToast(extractApiErrorMessage(error, '登录失败，请检查账号、密码或验证码'))
    if (captchaRequired.value) await refreshCaptcha()
  } finally {
    loading.value = false
  }
}

async function refreshCaptcha() {
  captchaLoading.value = true
  try {
    captcha.value = await loadCaptcha()
    captchaFallbackCode.value = randomCaptcha()
    form.captchaCode = ''
    errors.captcha = false
  } catch (error) {
    console.error(error)
    captchaFallbackCode.value = randomCaptcha()
    showToast('验证码加载失败，请刷新页面重试')
  } finally {
    captchaLoading.value = false
  }
}

function resolveRedirect(user: Awaited<ReturnType<typeof login>>) {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
  if (isHelp(redirect) || isOAuth(redirect)) return redirect
  if (redirect.startsWith('/') && !redirect.startsWith('//')) {
    const resolved = router.resolve(redirect)
    if (resolved.name !== 'not-found' && resolved.path !== '/login') return resolved.fullPath
  }
  return getPreferredEntryPath(user)
}

function isHelp(value: string) {
  return value === '/__aisys__/help' || value === '/__aisys__/help/' || value === '/__aisys__/help/user' || value === '/__aisys__/help/admin' || value.startsWith('/__aisys__/help/user/') || value.startsWith('/__aisys__/help/admin/')
}

function isOAuth(value: string) {
  try {
    const parsed = new URL(value, window.location.origin)
    return parsed.origin === window.location.origin && (parsed.pathname === '/oauth/authorize' || parsed.pathname === '/oauth/device')
  } catch {
    return false
  }
}

function startTypewriter() {
  if (reduceMotion) {
    typedIndex.value = typedChars.length - 1
    return
  }
  let index = -1
  const tick = () => {
    if (index < typedChars.length - 1) {
      index += 1
      typedIndex.value = index
      typeTimer = window.setTimeout(tick, 200)
    } else {
      typeTimer = window.setTimeout(deleteTick, 2600)
    }
  }
  const deleteTick = () => {
    if (index >= 0) {
      typedIndex.value = index - 1
      index -= 1
      typeTimer = window.setTimeout(deleteTick, 90)
    } else {
      typeTimer = window.setTimeout(() => {
        index = -1
        typedIndex.value = -1
        typeTimer = window.setTimeout(tick, 200)
      }, 1300)
    }
  }
  typeTimer = window.setTimeout(tick, 600)
}

function spawnFish(staticFish = false) {
  if (!fishLayer.value || (reduceMotion && !staticFish)) return
  const wrap = document.createElement('div')
  wrap.className = staticFish ? 'fish-jump static' : 'fish-jump'
  wrap.style.left = (14 + Math.random() * 72) + '%'
  const leap = 1.25 + Math.random() * 0.45
  wrap.style.setProperty('--leap', leap + 's')
  wrap.style.setProperty('--leap-h', '-' + Math.round(78 + Math.random() * 42) + 'px')
  const fish = document.createElement('span')
  fish.className = 'fish'
  const size = 0.68 + Math.random() * 0.62
  fish.style.width = Math.round(34 * size) + 'px'
  fish.style.height = Math.round(17 * size) + 'px'
  const facingRight = Math.random() < 0.25
  const travel = Math.round(64 + Math.random() * 70)
  if (facingRight) {
    fish.classList.add('flip')
    wrap.style.setProperty('--hx', travel + 'px')
    wrap.style.setProperty('--dir', '-1')
  } else {
    wrap.style.setProperty('--hx', '-' + travel + 'px')
    wrap.style.setProperty('--dir', '1')
  }
  fish.innerHTML = FISH_SVG
  const exit = document.createElement('span')
  exit.className = 'splash exit'
  exit.innerHTML = '<i class="drop d1"></i><i class="drop d2"></i><i class="drop d3"></i>'
  const land = document.createElement('span')
  land.className = 'splash land'
  land.innerHTML = '<i class="ring"></i><i class="drop d1"></i><i class="drop d2"></i><i class="drop d3"></i>'
  wrap.appendChild(fish)
  if (!staticFish) {
    wrap.appendChild(exit)
    wrap.appendChild(land)
  }
  fishLayer.value.appendChild(wrap)
  if (staticFish) return
  requestAnimationFrame(() => requestAnimationFrame(() => wrap.classList.add('jumping')))
  const leapMs = Math.round(leap * 1000)
  const exitTimer = window.setTimeout(() => exit.classList.add('burst'), Math.round(leapMs * 0.06))
  const landTimer = window.setTimeout(() => land.classList.add('burst'), Math.round(leapMs * 0.92))
  const removeTimer = window.setTimeout(() => wrap.remove(), leapMs + 800)
  fishTimers.push(exitTimer, landTimer, removeTimer)
}

function scheduleFish() {
  if (reduceMotion) return
  fishScheduleTimer = window.setTimeout(() => {
    spawnFish()
    scheduleFish()
  }, 2300 + Math.random() * 4300)
}

function onMountedReady() {
  rect = pageRef.value?.getBoundingClientRect()
  startTypewriter()
  if (reduceMotion) {
    spawnFish(true)
  } else {
    scheduleFish()
  }
}

onMounted(async () => {
  onMountedReady()
  await Promise.all([
    loadAppBrandSettings().catch((error) => console.error(error)),
    refreshCaptcha()
  ])
})

onBeforeUnmount(() => {
  if (frame) cancelAnimationFrame(frame)
  window.clearTimeout(toastTimer)
  window.clearTimeout(typeTimer)
  window.clearTimeout(fishScheduleTimer)
  fishTimers.forEach((timer) => window.clearTimeout(timer))
})
</script>

<style scoped>
.login-page {
  --bg: #f4f4f2;
  --surface: rgba(255, 255, 255, .6);
  --surface-strong: #ffffff;
  --white: #ffffff;
  --fg: #22282b;
  --muted: #8b9190;
  --border: rgba(34, 40, 43, .1);
  --border-strong: rgba(34, 40, 43, .24);
  --accent: #758789;
  --accent: oklch(.5 .04 210);
  --accent-deep: #53696b;
  --accent-deep: oklch(.38 .05 212);
  --coral: #a6755e;
  --coral: oklch(.55 .06 40);
  --danger: #9d5547;
  --danger: oklch(.5 .1 22);
  --focus-ring: rgba(82, 105, 107, .16);
  --focus-ring: oklch(.38 .05 212 / .16);
  --ease-in: cubic-bezier(.22, 1, .36, 1);
  --ease-bounce: cubic-bezier(.34, 1.56, .64, 1);
  --font-display: "Segoe UI Variable Display", "Helvetica Neue", "Avenir Next", "PingFang SC", sans-serif;
  --font-body: "Helvetica Neue", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
  --font-mono: "JetBrains Mono", ui-monospace, Menlo, Monaco, Consolas, monospace;
  position: relative;
  min-height: 100vh;
  min-height: 100dvh;
  overflow-x: hidden;
  isolation: isolate;
  color: var(--fg);
  background: var(--bg);
  font-family: var(--font-body);
  font-size: 14px;
  line-height: 1.6;
  -webkit-font-smoothing: antialiased;
}
.login-page *, .login-page *::before, .login-page *::after { box-sizing: border-box; }
.login-page button, .login-page input { font: inherit; }
.login-page button { cursor: pointer; }
.login-page h1, .login-page h2, .login-page h3, .login-page p { margin: 0; }
.login-page svg { display: block; }
.scene {
  position: relative;
  min-height: 100vh;
  min-height: 100dvh;
  isolation: isolate;
  display: grid;
  grid-template-rows: auto 1fr auto;
  padding: 0 clamp(24px, 6vw, 84px);
  background:
    radial-gradient(50% 50% at 84% 12%, rgba(220, 224, 224, .5), transparent 55%),
    radial-gradient(46% 46% at 10% 86%, rgba(200, 208, 206, .32), transparent 55%),
    var(--bg);
}
.glow { position: fixed; border-radius: 50%; filter: blur(70px); pointer-events: none; z-index: 0; opacity: .6; }
.glow.g1 { width: 320px; height: 320px; left: -60px; top: 20%; background: radial-gradient(circle, rgba(200, 214, 210, .7), transparent 70%); animation: drift-a 14s ease-in-out infinite; }
.glow.g2 { width: 280px; height: 280px; right: -50px; bottom: 16%; background: radial-gradient(circle, rgba(186, 200, 206, .6), transparent 70%); animation: drift-b 18s ease-in-out infinite; }
.masthead { position: relative; z-index: 2; padding: 22px 0 20px; animation: rise .7s ease .12s both; }
.masthead h1 { margin-top: 16px; font-family: var(--font-display); font-size: clamp(46px, 10.5vw, 132px); font-weight: 600; line-height: .94; letter-spacing: -.03em; color: var(--fg); }
.masthead h1 .line { display: block; overflow: hidden; }
.masthead h1 .line > span { display: inline-block; transform: translateY(.5em); opacity: 0; animation: reveal-line .72s var(--ease-in) both; }
.masthead h1 .line.l1 > span { animation-delay: .42s; }
.masthead h1 .line.l2 > span { animation-delay: .6s; }
.masthead h1 .thin { font-weight: 300; color: var(--muted); }
.brushline { width: min(300px, 42vw); height: 26px; margin-top: 26px; display: block; overflow: visible; opacity: 0; animation: brushline-in .5s ease .4s both; }
.brushline .lead { fill: none; stroke: var(--fg); stroke-width: 3; stroke-linecap: round; stroke-linejoin: round; stroke-dasharray: 240; stroke-dashoffset: 240; animation: draw 1.25s var(--ease-in) .5s forwards; }
.brushline .tail { fill: none; stroke: var(--accent-deep); stroke-width: 2; stroke-linecap: round; stroke-dasharray: 240; stroke-dashoffset: 240; animation: draw 1.25s var(--ease-in) .74s forwards; }
.brushline .dot { fill: var(--accent-deep); opacity: 0; animation: dot-in .5s ease 1.6s forwards; }
.body { position: relative; z-index: 2; display: flex; justify-content: center; align-items: center; padding-top: clamp(24px,5vh,72px); padding-bottom: 0; }
.left-note { position: fixed; top: clamp(8px,1.6vw,28px); right: clamp(12px,1.8vw,28px); width: clamp(180px,18vw,260px); color: var(--muted); z-index: 4; }
.hub { position: relative; width: 100%; aspect-ratio: 1; display: grid; place-items: center; }
.hub .ring { position: absolute; inset: 10%; border-radius: 50%; border: 1.5px solid var(--border-strong); opacity: 0; animation: ring-expand 1.3s var(--ease-in) .5s both; }
.hub .ring::before { content: ""; position: absolute; inset: -7px; border-radius: 50%; border: 1px solid var(--border); opacity: .8; }
.hub .core { width: 66px; height: 66px; border-radius: 50%; background: linear-gradient(135deg, var(--fg), var(--accent-deep)); display: grid; place-items: center; box-shadow: 0 14px 34px rgba(55,65,66,.28); z-index: 2; animation: core-in .7s var(--ease-bounce) .05s both, core-breathe 6s ease-in-out .8s infinite; }
.hub .core svg { width: 30px; height: 30px; }
.hub .orbit { position: absolute; inset: 0; animation: orbit-expand 1.3s var(--ease-in) .4s both, orbit-spin 46s linear 1.7s infinite; }
.hub .node { position: absolute; width: 44px; height: 44px; border-radius: 50%; background: var(--surface-strong); border: 1px solid var(--border); display: grid; place-items: center; box-shadow: 0 6px 18px rgba(60,70,68,.09); z-index: 1; transform: translate(-50%,-50%); opacity: 0; animation: node-in .5s ease both; }
.hub .node:nth-child(1) { animation-delay: .55s; } .hub .node:nth-child(2) { animation-delay: .65s; } .hub .node:nth-child(3) { animation-delay: .75s; } .hub .node:nth-child(4) { animation-delay: .85s; } .hub .node:nth-child(5) { animation-delay: .95s; } .hub .node:nth-child(6) { animation-delay: 1.05s; } .hub .node:nth-child(7) { animation-delay: 1.15s; } .hub .node:nth-child(8) { animation-delay: 1.25s; }
.hub .node svg { width: 20px; height: 20px; display: block; animation: node-counter var(--dur,46s) linear infinite; }
.auth-wrap { width: min(100%,420px); margin-top: clamp(-176px, calc(-10vh - 46px), -130px); animation: rise .7s ease .22s both; }
.auth-icons { display: flex; align-items: center; justify-content: center; gap: 12px; margin-bottom: 26px; }
.auth-icons .brand-mark { width: 34px; height: 34px; border-radius: 50%; background: linear-gradient(135deg,var(--fg),var(--accent-deep)); display: grid; place-items: center; box-shadow: 0 6px 16px rgba(55,65,66,.22); transition: transform 200ms ease, box-shadow 200ms ease; }
.auth-icons .brand-mark svg { width: 17px; height: 17px; }
.auth-icons .brand-mark:hover { transform: translateY(-2px); box-shadow: 0 10px 22px rgba(55,65,66,.3); }
.auth-icons .login-word { font-family: var(--font-display); font-size: 20px; font-weight: 600; letter-spacing: .06em; color: var(--fg); line-height: 1; }
.field { margin-bottom: 16px; opacity: 0; animation: rise .55s ease both; }
.field:nth-child(1) { animation-delay: .3s; } .field:nth-child(2) { animation-delay: .4s; } .field:nth-child(3) { animation-delay: .5s; }
.field-row { display: flex; align-items: baseline; justify-content: space-between; gap: 14px; margin-bottom: 7px; }
.field-label { font-size: 11px; font-weight: 600; color: var(--fg); letter-spacing: .05em; }
.field-hint { color: var(--muted); font-family: var(--font-mono); font-size: 10px; letter-spacing: .08em; }
.input-wrap { position: relative; }
.login-page .input { width: 100%; height: 52px; padding: 0 14px; border: 1px solid var(--border); border-radius: 12px; outline: none; background: var(--surface); color: var(--fg); font-size: 14px; font-weight: 500; box-shadow: inset 0 1px 2px rgba(60,70,68,.04); transition: border-color 150ms ease, background 150ms ease, box-shadow 150ms ease; }
.input::placeholder { color: var(--muted); }
.input:hover { border-color: var(--border-strong); }
.input:focus { border-color: var(--accent-deep); background: var(--white); box-shadow: 0 0 0 3px var(--focus-ring); }
/* Keep password-manager autofill, but make its painted state match the prototype surface. */
.input:-webkit-autofill,
.input:-webkit-autofill:hover,
.input:-webkit-autofill:active {
  -webkit-text-fill-color: var(--fg);
  caret-color: var(--fg);
  -webkit-box-shadow: 0 0 0 1000px var(--white) inset;
  box-shadow: 0 0 0 1000px var(--white) inset;
  border-color: var(--border);
}
.input:-webkit-autofill:focus {
  -webkit-text-fill-color: var(--fg);
  caret-color: var(--fg);
  -webkit-box-shadow: 0 0 0 3px var(--focus-ring), 0 0 0 1000px var(--white) inset;
  box-shadow: 0 0 0 3px var(--focus-ring), 0 0 0 1000px var(--white) inset;
  border-color: var(--accent-deep);
}
.input:-moz-autofill {
  color: var(--fg);
  caret-color: var(--fg);
  box-shadow: 0 0 0 1000px var(--white) inset;
  border-color: var(--border);
}
.input:-moz-autofill:focus {
  box-shadow: 0 0 0 3px var(--focus-ring), 0 0 0 1000px var(--white) inset;
  border-color: var(--accent-deep);
}
.input.has-toggle { padding-right: 48px; }
.input-toggle { position: absolute; top: 50%; right: 8px; width: 34px; height: 34px; display: grid; place-items: center; transform: translateY(-50%); border: none; border-radius: 8px; background: transparent; color: var(--muted); transition: color 150ms ease, background 150ms ease; }
.input-toggle:hover { color: var(--fg); background: rgba(34,40,43,.05); }
.input-toggle svg { width: 16px; height: 16px; }
.captcha-row { display: grid; grid-template-columns: minmax(0,1fr) 108px; gap: 10px; }
.login-page .captcha-button { height: 52px; padding: 0; border: 1px solid var(--border); border-radius: 12px; background: var(--surface-strong); color: var(--fg); font-family: var(--font-mono); font-size: 14px; font-weight: 700; letter-spacing: .16em; transition: border-color 150ms ease; overflow: hidden; }
.captcha-button:hover:not(:disabled) { border-color: var(--accent); }
.captcha-button[aria-busy="true"] { opacity: .55; pointer-events: none; }
.captcha-button img { width: 100%; height: 100%; display: block; object-fit: contain; image-rendering: auto; }
.login-page .captcha-button span { width: 100%; height: 100%; display: inline-flex; align-items: center; justify-content: center; color: var(--fg); font-family: var(--font-mono); font-size: 14px; font-weight: 700; letter-spacing: .16em; line-height: 1; }
.field-error { display: none; margin-top: 5px; color: var(--danger); font-size: 11px; font-weight: 600; }
.field.has-error .field-error { display: block; animation: error-in .24s var(--ease-in) both; }
.field.has-error .input, .field.has-error .captcha-button { border-color: var(--danger); }
.login-page .submit-button { position: relative; width: 100%; min-height: 54px; display: inline-flex; align-items: center; justify-content: center; gap: 9px; border: none; border-radius: 12px; background: linear-gradient(135deg,var(--fg),var(--accent-deep)); color: var(--white); font-family: var(--font-display); font-size: 14px; font-weight: 600; letter-spacing: .04em; box-shadow: inset 0 1px 0 rgba(255,255,255,.12), 0 10px 24px rgba(55,65,66,.16); transition: transform 150ms ease, background 150ms ease, box-shadow 150ms ease, filter 150ms ease; overflow: hidden; opacity: 0; animation: rise .55s ease .58s both; }
.submit-button::before { content: ""; position: absolute; top: 0; bottom: 0; left: -70%; width: 40%; background: linear-gradient(100deg,transparent,rgba(255,255,255,.22),transparent); transform: skewX(-18deg); transition: left 600ms ease; pointer-events: none; }
.submit-button:hover:not(:disabled) { transform: translateY(-2px); filter: brightness(1.05); box-shadow: inset 0 1px 0 rgba(255,255,255,.12), 0 16px 32px rgba(55,65,66,.24); }
.submit-button:hover:not(:disabled)::before { left: 135%; }
.submit-button:active { transform: translateY(0); }
.submit-button:disabled { cursor: wait; opacity: .7; transform: none; box-shadow: none; }
.spinner { width: 16px; height: 16px; border: 2px solid rgba(255,255,255,.4); border-top-color: var(--white); border-radius: 50%; animation: spin 700ms linear infinite; }
.type-note { margin-top: 18px; min-height: 20px; display: flex; flex-wrap: wrap; align-items: center; justify-content: center; column-gap: 2px; row-gap: 6px; padding: 0 2px; line-height: 1.5; font-size: 12.5px; color: var(--muted); letter-spacing: .04em; }
.type-note .type-char { opacity: 0; transition: opacity 140ms linear; }
.type-note .type-char.in { opacity: 1; }
.type-note .type-char.out { opacity: 0; }
.type-note .type-caret { width: 1.5px; height: 14px; background: var(--accent-deep); display: inline-block; animation: caret-blink 1s steps(2,start) infinite; }
.shoreline { position: relative; z-index: 0; height: 84px; margin-top: auto; margin-left: calc(clamp(24px,6vw,84px) * -1); margin-right: calc(clamp(24px,6vw,84px) * -1); pointer-events: none; overflow: hidden; }
.wave-track { position: absolute; left: 0; bottom: 0; width: 300%; height: 100%; display: flex; }
.wave-group { width: 33.3333%; height: 100%; flex: 0 0 33.3333%; }
.wave-group svg { width: 100%; height: 100%; display: block; overflow: hidden; }
.wave-group path { fill: none; stroke-linecap: round; stroke-linejoin: round; }
.wave-group .w1 { stroke: var(--fg); stroke-width: 1.4; opacity: .44; }
.wave-group .w2 { stroke: var(--accent-deep); stroke-width: 1; opacity: .3; }
.wave-group .w3 { stroke: var(--muted); stroke-width: .9; opacity: .2; }
.wave-track.running { animation: wave-flow 9s linear infinite; }
.boat { position: absolute; left: 7%; bottom: 30px; z-index: 3; width: 92px; height: 44px; pointer-events: none; }
.boat svg { width: 100%; height: 100%; overflow: visible; }
.boat .boat-body { transform-origin: 50% 88%; animation: boat-bob 5.2s ease-in-out infinite; }
.boat .figure { transform-origin: 50% 90%; animation: boat-lean 5.2s ease-in-out infinite; }
.fish-layer { position: absolute; left: 0; right: 0; bottom: 0; height: 176px; z-index: 4; pointer-events: none; overflow: hidden; }
:global(.fish-jump) { position: absolute; bottom: 10px; width: 0; height: 0; }
:global(.fish-jump .fish) { position: absolute; left: 0; bottom: 0; width: 34px; height: 17px; transform: translate(-50%, 0); opacity: 0; color: var(--fg); }
:global(.fish-jump.static .fish) { opacity: .92; }
:global(.fish-jump .fish svg) { width: 100%; height: 100%; overflow: visible; }
:global(.fish-jump .fish.flip svg) { transform: scaleX(-1); }
:global(.fish-jump.jumping .fish) { animation: fish-leap var(--leap,1.35s) linear both; }
:global(.fish-jump .splash) { position: absolute; bottom: -2px; width: 0; height: 0; }
:global(.fish-jump .splash.exit) { left: 0; }
:global(.fish-jump .splash.land) { left: var(--hx,-60px); }
:global(.fish-jump .splash .ring) { position: absolute; left: -13px; bottom: -4px; width: 26px; height: 9px; border: 1.5px solid var(--accent-deep); border-radius: 50%; opacity: 0; }
:global(.fish-jump .splash .drop) { position: absolute; left: 0; bottom: 0; width: 3px; height: 3px; border-radius: 50%; background: var(--accent-deep); opacity: 0; }
:global(.fish-jump .drop.d1) { --dx: -16px; --dy: -22px; } :global(.fish-jump .drop.d2) { --dx: -2px; --dy: -30px; } :global(.fish-jump .drop.d3) { --dx: 14px; --dy: -20px; }
:global(.fish-jump .splash.burst .ring) { animation: ring-out .5s ease-out both; }
:global(.fish-jump .splash.burst .drop) { animation: drop-up .55s ease-out both; }
:global(.fish-jump .splash.burst .drop.d2) { animation-delay: .05s; } :global(.fish-jump .splash.burst .drop.d3) { animation-delay: .1s; }
.toast { position: fixed; right: 24px; bottom: 64px; z-index: 20; max-width: min(360px,calc(100vw - 48px)); padding: 13px 18px; border: 1px solid var(--border); border-radius: 12px; background: var(--surface-strong); color: var(--fg); box-shadow: 0 18px 44px rgba(60,70,64,.12); font-size: 13px; font-weight: 500; animation: toast-in .16s ease both; }
.input:focus-visible, .input-toggle:focus-visible, .captcha-button:focus-visible, .submit-button:focus-visible { outline: 2px solid var(--accent-deep); outline-offset: 2px; }
@keyframes reveal-line { to { transform: translateY(0); opacity: 1; } } @keyframes brushline-in { to { opacity: 1; } } @keyframes draw { to { stroke-dashoffset: 0; } } @keyframes dot-in { from { opacity: 0; transform: scale(.3); } to { opacity: .9; transform: scale(1); } }
@keyframes ring-expand { from { transform: scale(.1); opacity: 0; } to { transform: scale(1); opacity: 1; } } @keyframes core-in { from { transform: scale(0); opacity: 0; } to { transform: scale(1); opacity: 1; } } @keyframes core-breathe { 0%,100% { transform: scale(1); box-shadow: 0 14px 34px rgba(55,65,66,.28); } 50% { transform: scale(1.04); box-shadow: 0 18px 40px rgba(55,65,66,.26); } }
@keyframes orbit-expand { from { transform: scale(0); opacity: 0; } to { transform: scale(1); opacity: 1; } } @keyframes orbit-spin { from { transform: scale(1) rotate(0deg); } to { transform: scale(1) rotate(360deg); } } @keyframes node-in { from { opacity: 0; transform: translate(-50%,-50%) scale(.2); } to { opacity: 1; transform: translate(-50%,-50%) scale(1); } } @keyframes node-counter { from { transform: rotate(0deg); } to { transform: rotate(-360deg); } }
@keyframes wave-flow { to { transform: translateX(-33.3333%); } } @keyframes boat-bob { 0%,100% { transform: translateY(0) rotate(-.5deg); } 50% { transform: translateY(-3px) rotate(.5deg); } } @keyframes boat-lean { 0%,100% { transform: rotate(-1.2deg); } 50% { transform: rotate(1.2deg); } }
@keyframes fish-leap { 0% { transform: translate(-50%,0) rotate(0); opacity: 0; animation-timing-function: cubic-bezier(.22,.61,.36,1); } 8% { opacity: 1; } 26% { transform: translate(calc(-50% + var(--hx,-60px) * .2), calc(var(--leap-h,-96px) * .66)) rotate(calc(var(--dir,1) * 24deg)); } 50% { transform: translate(calc(-50% + var(--hx,-60px) * .5), var(--leap-h,-96px)) rotate(0); animation-timing-function: cubic-bezier(.5,.05,.68,.19); } 78% { transform: translate(calc(-50% + var(--hx,-60px) * .82), calc(var(--leap-h,-96px) * .32)) rotate(calc(var(--dir,1) * -24deg)); } 97% { opacity: 1; } 100% { transform: translate(calc(-50% + var(--hx,-60px)),0) rotate(calc(var(--dir,1) * -10deg)); opacity: 0; } }
@keyframes ring-out { 0% { opacity: 0; transform: scale(.35); } 35% { opacity: .6; } 100% { opacity: 0; transform: scale(2.4); } } @keyframes drop-up { 0% { opacity: 0; transform: translate(-50%,-50%); } 30% { opacity: .9; transform: translate(calc(-50% + var(--dx,0px) * .55), calc(-50% + var(--dy,-18px))); } 100% { opacity: 0; transform: translate(calc(-50% + var(--dx,0px)), calc(-50% + var(--dy,-18px) * .4)); } }
@keyframes spin { to { transform: rotate(360deg); } } @keyframes caret-blink { 0%,100% { opacity: 1; } 50% { opacity: 0; } } @keyframes error-in { from { opacity: 0; transform: translateY(-3px); } to { opacity: 1; transform: translateY(0); } } @keyframes toast-in { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } } @keyframes rise { from { opacity: 0; transform: translateY(18px); } to { opacity: 1; transform: translateY(0); } } @keyframes drift-a { 0%,100% { transform: translate(0,0) scale(1); opacity: .5; } 50% { transform: translate(60px,-40px) scale(1.15); opacity: .75; } } @keyframes drift-b { 0%,100% { transform: translate(0,0) scale(1); opacity: .45; } 50% { transform: translate(-70px,50px) scale(1.1); opacity: .7; } }
@media (max-width:900px) {
  .scene { padding-left: clamp(16px,5vw,24px); padding-right: clamp(16px,5vw,24px); }
  .masthead { padding: 18px 0 22px; }
  .masthead h1 { font-size: clamp(32px,10vw,46px); }
  .body { padding-top: 16px; }
  .left-note { top: clamp(10px,3vw,20px); right: clamp(10px,3vw,20px); width: clamp(104px,29vw,140px); pointer-events: none; }
  .hub .core { width: 44px; height: 44px; } .hub .core svg { width: 20px; height: 20px; } .hub .node { width: 28px; height: 28px; } .hub .node svg { width: 13px; height: 13px; }
  .auth-wrap { width: 100%; max-width: 420px; margin: 0 auto; min-width: 0; }
  .input, .submit-button, .captcha-button { width: 100%; min-width: 0; max-width: 100%; }
  .field-row { flex-wrap: wrap; row-gap: 2px; column-gap: 10px; } .captcha-row { grid-template-columns: 1fr; min-width: 0; }
  .shoreline { height: 64px; } .boat { width: 68px; height: 34px; left: 10%; bottom: 22px; }
}
@media (prefers-reduced-motion: reduce) {
  .login-page *, .login-page *::before, .login-page *::after { animation-duration: .001ms !important; animation-iteration-count: 1 !important; transition-duration: .001ms !important; }
  .hub .core, .hub .orbit, .hub .ring, .hub .node { animation: none !important; opacity: 1 !important; transform: none !important; }
  .hub .node { transform: translate(-50%,-50%) !important; }
  .masthead h1 .line > span { opacity: 1 !important; transform: none !important; } .brushline { opacity: 1 !important; } .brushline path { stroke-dashoffset: 0 !important; } .brushline .dot { opacity: .9 !important; }
}
</style>
